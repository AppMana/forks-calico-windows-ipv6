/*
 * hns-ipv6-hook: filter iphlpapi!GetAdaptersAddresses inside the HNS
 * service process so HNS sees only the operator-configured "desired"
 * IPv6 management address when scanning the NIC. HNS then pins that
 * address as the L2Bridge network's ManagementIPv6 — without us
 * having to remove anything from the live NIC.
 *
 * Configuration: the desired IPv6 address (no prefix length) is read
 * at DllMain time from C:\CalicoWindows\hns-ipv6-hook.cfg. The file
 * holds the textual IPv6 only ("fd5a:8000:1:0:1ac0:4dff:fe89:5194\n").
 * Services don't inherit caller envvars; a file is the simplest way
 * to pass config to an injected DLL.
 *
 * Cross-compile (Linux, MinGW-w64):
 *   x86_64-w64-mingw32-gcc -O2 -shared -o hns-ipv6-hook.dll hook.c \
 *       -Wl,--enable-stdcall-fixup -lws2_32 -liphlpapi -static-libgcc
 *
 * Build target: Windows Server 2022, build 20348.x (LTSC2022). The
 * trampoline implementation here uses a length-aware copy of the
 * GetAdaptersAddresses prologue. We refuse to install if the prologue
 * doesn't match the known-good byte pattern for that build.
 */

#define WIN32_LEAN_AND_MEAN
#define _WIN32_WINNT 0x0A00
#include <winsock2.h>
#include <ws2tcpip.h>
#include <windows.h>
#include <iphlpapi.h>
#include <stdarg.h>
#include <stdio.h>
#include <string.h>

typedef ULONG (WINAPI *GetAdaptersAddresses_t)(ULONG, ULONG, PVOID,
                                               PIP_ADAPTER_ADDRESSES, PULONG);

static unsigned char *g_trampoline = NULL;
static unsigned char  g_desired[16];
static int            g_have_desired = 0;
static volatile LONG  g_filter_active = 0;

static void hlog(const char *fmt, ...)
{
    char buf[1024];
    va_list ap;
    va_start(ap, fmt);
    vsnprintf(buf, sizeof(buf), fmt, ap);
    va_end(ap);
    HANDLE h = CreateFileA("C:\\hns-ipv6-hook.log", FILE_APPEND_DATA, FILE_SHARE_READ,
                           NULL, OPEN_ALWAYS, FILE_ATTRIBUTE_NORMAL, NULL);
    if (h != INVALID_HANDLE_VALUE) {
        DWORD written;
        SetFilePointer(h, 0, NULL, FILE_END);
        WriteFile(h, buf, (DWORD)strlen(buf), &written, NULL);
        WriteFile(h, "\r\n", 2, &written, NULL);
        CloseHandle(h);
    }
}

static int read_desired_from_file(unsigned char out[16])
{
    HANDLE h = CreateFileA("C:\\CalicoWindows\\hns-ipv6-hook.cfg", GENERIC_READ,
                           FILE_SHARE_READ, NULL, OPEN_EXISTING, FILE_ATTRIBUTE_NORMAL, NULL);
    if (h == INVALID_HANDLE_VALUE) return 0;
    char buf[80];
    DWORD got = 0;
    BOOL ok = ReadFile(h, buf, sizeof(buf) - 1, &got, NULL);
    CloseHandle(h);
    if (!ok || got == 0) return 0;
    buf[got] = 0;
    /* trim trailing whitespace/newline */
    for (DWORD i = 0; i < got; i++) {
        if (buf[i] == '\r' || buf[i] == '\n' || buf[i] == ' ' || buf[i] == '\t') {
            buf[i] = 0;
            break;
        }
    }
    struct in6_addr a;
    if (InetPtonA(AF_INET6, buf, &a) != 1) return 0;
    memcpy(out, &a, 16);
    return 1;
}

static int is_link_local(const unsigned char *addr)
{
    return (addr[0] == 0xFE && (addr[1] & 0xC0) == 0x80);
}

/* Detour: leave only the exact desired IPv6 address visible to the
 * caller. Drop every other IPv6 unicast — INCLUDING link-local. HNS
 * picks ManagementIPv6 by taking the first non-link-local IPv6 it
 * sees, but because we observed it can also fall back to a link-local
 * with a scope ID when no other IPv6 is present, we drop link-local
 * too. The IPv4 list is left untouched.
 *
 * If desired is the all-zeros address (parse failure / not loaded),
 * we leave the list alone so we don't accidentally hide every IPv6
 * from every consumer. */
static ULONG WINAPI hooked_GetAdaptersAddresses(ULONG family, ULONG flags, PVOID reserved,
                                                PIP_ADAPTER_ADDRESSES buf, PULONG sz)
{
    GetAdaptersAddresses_t orig = (GetAdaptersAddresses_t)g_trampoline;
    ULONG ret = orig(family, flags, reserved, buf, sz);
    if (ret != NO_ERROR || !buf) return ret;
    if (!InterlockedCompareExchange(&g_filter_active, 0, 0)) return ret;

    /* Safety: don't filter if desired is all-zeros. */
    static const unsigned char zero[16] = {0};
    if (memcmp(g_desired, zero, 16) == 0) return ret;

    for (PIP_ADAPTER_ADDRESSES a = buf; a; a = a->Next) {
        PIP_ADAPTER_UNICAST_ADDRESS *prev = &a->FirstUnicastAddress;
        PIP_ADAPTER_UNICAST_ADDRESS u = a->FirstUnicastAddress;
        while (u) {
            int drop = 0;
            if (u->Address.lpSockaddr &&
                u->Address.lpSockaddr->sa_family == AF_INET6) {
                struct sockaddr_in6 *sa = (struct sockaddr_in6 *)u->Address.lpSockaddr;
                unsigned char *ip = (unsigned char *)&sa->sin6_addr;
                if (memcmp(ip, g_desired, 16) != 0) {
                    drop = 1;
                }
            }
            PIP_ADAPTER_UNICAST_ADDRESS next = u->Next;
            if (drop) {
                *prev = next;
            } else {
                prev = &u->Next;
            }
            u = next;
        }
    }
    return ret;
}

/*
 * Length-aware prologue parser for the exact byte sequence used by
 * iphlpapi!GetAdaptersAddresses on Windows Server 2022, LTSC2022,
 * build 20348.4773 — verified by reading the live process memory.
 * We are NOT a general disassembler; we accept ONLY this sequence
 * (or a length-equivalent one whose instructions are all relocatable)
 * and bail otherwise. The injector also gates on Server 2022 build,
 * so a mismatch here means an OS update changed the function and the
 * hook needs re-verification.
 *
 * Verified prologue:
 *   48 89 5C 24 18         mov [rsp+0x18], rbx     (5)
 *   55                      push rbp                 (1)
 *   56                      push rsi                 (1)
 *   57                      push rdi                 (1)
 *   41 56                   push r14                 (2)
 *   41 57                   push r15                 (2)
 *   48 8B EC                mov rbp, rsp             (3)
 *   = 15 bytes, all position-independent, fits our 14-byte abs-JMP.
 *
 * Returns the byte count to copy (>= 14) or 0 if unrecognised.
 */
static int recognised_prologue_len(const unsigned char *p)
{
    /* mov [rsp+0x18], rbx -- 48 89 5C 24 18 */
    if (p[0] != 0x48 || p[1] != 0x89 || p[2] != 0x5C ||
        p[3] != 0x24 || p[4] != 0x18) return 0;
    int off = 5;
    /* push rbp / rsi / rdi (1 byte opcodes 0x55, 0x56, 0x57). */
    if (p[off++] != 0x55) return 0;
    if (p[off++] != 0x56) return 0;
    if (p[off++] != 0x57) return 0;
    /* push r14 -- 41 56, push r15 -- 41 57 (REX.B + 1-byte push). */
    if (p[off] != 0x41 || p[off + 1] != 0x56) return 0;
    off += 2;
    if (p[off] != 0x41 || p[off + 1] != 0x57) return 0;
    off += 2;
    /* mov rbp, rsp -- 48 8B EC */
    if (p[off] != 0x48 || p[off + 1] != 0x8B || p[off + 2] != 0xEC) return 0;
    off += 3;
    return off; /* 15 */
}

/* Install a 14-byte FF 25 absolute jump.
 *   target_fn:  function to redirect (mutable code page).
 *   detour_fn:  our function that takes the same args.
 * Returns trampoline (callable as the original) or NULL on failure. */
static unsigned char *install_absolute_jmp(void *target_fn, void *detour_fn)
{
    int prologue = recognised_prologue_len((unsigned char *)target_fn);
    if (prologue < 14) {
        hlog("hns-ipv6-hook: unrecognised prologue at %p, refusing to patch", target_fn);
        return NULL;
    }
    /* We need to copy the entire prologue bytes (>= 14, <= 15) and
     * resume execution at target+prologue. Allocate trampoline page. */
    SYSTEM_INFO si;
    GetSystemInfo(&si);
    unsigned char *tramp = (unsigned char *)VirtualAlloc(
        NULL, si.dwPageSize, MEM_COMMIT | MEM_RESERVE,
        PAGE_EXECUTE_READWRITE);
    if (!tramp) return NULL;

    memcpy(tramp, target_fn, prologue);
    /* JMP [rip+0]; <8 byte abs target> after copied prologue. */
    tramp[prologue + 0] = 0xFF;
    tramp[prologue + 1] = 0x25;
    *(unsigned int *)(tramp + prologue + 2) = 0;
    *(void **)(tramp + prologue + 6) = (unsigned char *)target_fn + prologue;

    DWORD oldProt;
    if (!VirtualProtect(target_fn, 14, PAGE_EXECUTE_READWRITE, &oldProt)) {
        VirtualFree(tramp, 0, MEM_RELEASE);
        return NULL;
    }
    unsigned char *t = (unsigned char *)target_fn;
    t[0] = 0xFF;
    t[1] = 0x25;
    *(unsigned int *)(t + 2) = 0;
    *(void **)(t + 6) = detour_fn;
    DWORD junk;
    VirtualProtect(target_fn, 14, oldProt, &junk);
    FlushInstructionCache(GetCurrentProcess(), target_fn, 14);

    return tramp;
}

BOOL WINAPI DllMain(HINSTANCE hInst, DWORD reason, LPVOID reserved)
{
    if (reason != DLL_PROCESS_ATTACH) return TRUE;
    DisableThreadLibraryCalls(hInst);

    if (!read_desired_from_file(g_desired)) {
        hlog("hns-ipv6-hook: no desired ManagementIPv6 from cfg file; not hooking");
        return TRUE;
    }
    g_have_desired = 1;

    HMODULE iph = LoadLibraryA("iphlpapi.dll");
    if (!iph) { hlog("hns-ipv6-hook: LoadLibrary iphlpapi.dll failed: %lu", GetLastError()); return TRUE; }
    void *target = (void *)GetProcAddress(iph, "GetAdaptersAddresses");
    if (!target) { hlog("hns-ipv6-hook: GetProcAddress failed"); return TRUE; }

    g_trampoline = install_absolute_jmp(target, (void *)hooked_GetAdaptersAddresses);
    if (!g_trampoline) { hlog("hns-ipv6-hook: trampoline install failed"); return TRUE; }

    InterlockedExchange(&g_filter_active, 1);
    char dbg[64];
    InetNtopA(AF_INET6, g_desired, dbg, sizeof(dbg));
    hlog("hns-ipv6-hook: installed at %p, desired ManagementIPv6=%s", target, dbg);
    return TRUE;
}
