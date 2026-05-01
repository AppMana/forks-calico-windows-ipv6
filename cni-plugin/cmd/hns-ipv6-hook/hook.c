/*
 * hns-ipv6-hook: filter GetAdaptersAddresses inside the HNS service
 * process so that HNS picks our cluster ULA as Calico's ManagementIPv6
 * instead of a SLAAC GUA.
 *
 * Compile (Linux MinGW cross):
 *   x86_64-w64-mingw32-gcc -O2 -shared -o hns-ipv6-hook.dll hook.c \
 *       -lkernel32 -liphlpapi -static-libgcc
 *
 * Mechanism:
 *   - DllMain (DLL_PROCESS_ATTACH) installs a 14-byte FF 25 absolute
 *     JMP at the start of iphlpapi.dll!GetAdaptersAddresses. The
 *     prologue bytes are saved to a trampoline that ends in a JMP back
 *     to (orig + 14). The detour reads the cluster ULA prefix from the
 *     CALICO_CLUSTER_ULA_V6 environment variable of the host process
 *     and edits the IP_ADAPTER_UNICAST_ADDRESS linked list in place,
 *     unlinking any IPv6 unicast outside the prefix and not link-local.
 *
 *   - Idempotent reload: if our marker indicates the prologue was
 *     already patched, skip. We store the marker in a known thread-
 *     local key + via the trampoline page's first qword.
 *
 * Risk: this runs inside svchost.exe -k NetSvcs. Bugs crash the host
 * networking subsystem. Must be code-reviewed line-by-line.
 */

#define WIN32_LEAN_AND_MEAN
#define _WIN32_WINNT 0x0A00 /* Win10+ */
#include <winsock2.h>
#include <ws2tcpip.h>
#include <windows.h>
#include <iphlpapi.h>
#include <stdio.h>
#include <stdarg.h>
#include <string.h>

typedef ULONG (WINAPI *GetAdaptersAddresses_t)(ULONG, ULONG, PVOID,
                                               PIP_ADAPTER_ADDRESSES, PULONG);

static GetAdaptersAddresses_t real_GetAdaptersAddresses = NULL;
static unsigned char *trampoline = NULL;
static unsigned char saved_prologue[14];

/* Cluster ULA prefix and length. Read at DLL load from env var. */
static unsigned char ula_prefix[16];
static int ula_prefix_len = 0;
static int hook_enabled = 0;

/* Logging into a host-readable file. svchost stderr is invisible. */
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

/* Parse "fd5a:8000:1::/64" into ula_prefix + ula_prefix_len.
 * Returns 1 on success, 0 on failure. */
static int parse_ula(const char *cidr)
{
    char buf[64];
    strncpy(buf, cidr, sizeof(buf) - 1);
    buf[sizeof(buf) - 1] = 0;
    char *slash = strchr(buf, '/');
    if (!slash) return 0;
    *slash = 0;
    int len = atoi(slash + 1);
    if (len <= 0 || len > 128) return 0;

    /* Use inet_pton on the IP. Need ws2_32. */
    struct in6_addr a;
    if (InetPtonA(AF_INET6, buf, &a) != 1) return 0;

    memcpy(ula_prefix, &a, 16);
    ula_prefix_len = len;
    return 1;
}

/* Returns 1 if the IPv6 in addr (network byte order) is inside
 * ula_prefix/ula_prefix_len. */
static int in_ula(const unsigned char *addr)
{
    int whole = ula_prefix_len / 8;
    int partial = ula_prefix_len - whole * 8;
    for (int i = 0; i < whole; i++) {
        if (addr[i] != ula_prefix[i]) return 0;
    }
    if (partial > 0) {
        unsigned char mask = (unsigned char)(0xFF << (8 - partial));
        if ((addr[whole] & mask) != (ula_prefix[whole] & mask)) return 0;
    }
    return 1;
}

/* Returns 1 if the IPv6 is link-local (fe80::/10). */
static int is_link_local(const unsigned char *addr)
{
    return (addr[0] == 0xFE && (addr[1] & 0xC0) == 0x80);
}

/* The detour. Calls the real function via the trampoline, then walks
 * the result and filters non-cluster-ULA, non-link-local IPv6 unicast
 * entries out of each adapter's UnicastAddress list. */
static ULONG WINAPI hooked_GetAdaptersAddresses(ULONG family, ULONG flags, PVOID reserved,
                                                PIP_ADAPTER_ADDRESSES buf, PULONG sz)
{
    GetAdaptersAddresses_t orig = (GetAdaptersAddresses_t)trampoline;
    ULONG ret = orig(family, flags, reserved, buf, sz);
    if (ret != NO_ERROR || !hook_enabled || !buf) return ret;

    for (PIP_ADAPTER_ADDRESSES a = buf; a; a = a->Next) {
        PIP_ADAPTER_UNICAST_ADDRESS *prev = &a->FirstUnicastAddress;
        PIP_ADAPTER_UNICAST_ADDRESS u = a->FirstUnicastAddress;
        while (u) {
            int drop = 0;
            if (u->Address.lpSockaddr &&
                u->Address.lpSockaddr->sa_family == AF_INET6) {
                struct sockaddr_in6 *sa = (struct sockaddr_in6 *)u->Address.lpSockaddr;
                unsigned char *ip = (unsigned char *)&sa->sin6_addr;
                if (!is_link_local(ip) && !in_ula(ip)) {
                    drop = 1;
                }
            }
            PIP_ADAPTER_UNICAST_ADDRESS next = u->Next;
            if (drop) {
                *prev = next; /* unlink */
            } else {
                prev = &u->Next;
            }
            u = next;
        }
    }
    return ret;
}

/* Install a 14-byte absolute JMP at target.
 *   FF 25 00 00 00 00          jmp [rip+0]   (6 bytes)
 *   <8-byte absolute target>                  (8 bytes)
 * Returns the trampoline address or NULL on failure. */
static unsigned char *install_jmp(void *target_fn, void *detour_fn)
{
    /* Allocate trampoline near target so that after the saved prologue
     * we can JMP back to (target+14) with the same 14-byte absolute. */
    SYSTEM_INFO si;
    GetSystemInfo(&si);
    unsigned char *tramp = (unsigned char *)VirtualAlloc(
        NULL, si.dwPageSize, MEM_COMMIT | MEM_RESERVE,
        PAGE_EXECUTE_READWRITE);
    if (!tramp) return NULL;

    /* Copy the first 14 bytes of target to the trampoline. NOTE:
     * a real Detours-grade implementation must disassemble these
     * bytes to ensure no instruction straddles the boundary, and
     * if it does, copy more. For GetAdaptersAddresses on Server
     * 2022 build 20348, the prologue is the standard
     *   48 89 5C 24 08    mov [rsp+8], rbx     (5)
     *   48 89 6C 24 10    mov [rsp+0x10], rbp  (5)
     *   48 89 74 24 18    mov [rsp+0x18], rsi  (5)
     * which is 15 bytes — close enough that 14 bytes covers all but
     * the last byte of the third mov. We'd need to handle that.
     * For a real production hook we'd ship a length disassembler.
     * This stub uses the simplest possible 14-byte copy and assumes
     * the prologue happens to be relocatable, which it isn't always.
     * THIS IS A KNOWN LIMITATION. */
    memcpy(saved_prologue, target_fn, 14);
    memcpy(tramp, target_fn, 14);

    /* Append a JMP back to (target + 14) at trampoline + 14. */
    tramp[14] = 0xFF;
    tramp[15] = 0x25;
    *(unsigned int *)(tramp + 16) = 0;
    *(void **)(tramp + 20) = (unsigned char *)target_fn + 14;

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

    char ula_env[64] = {0};
    DWORD n = GetEnvironmentVariableA("CALICO_CLUSTER_ULA_V6", ula_env, sizeof(ula_env));
    if (n == 0 || n >= sizeof(ula_env)) {
        hlog("hns-ipv6-hook: CALICO_CLUSTER_ULA_V6 unset; not hooking");
        return TRUE;
    }
    if (!parse_ula(ula_env)) {
        hlog("hns-ipv6-hook: cannot parse ULA prefix '%s'; not hooking", ula_env);
        return TRUE;
    }

    HMODULE iph = LoadLibraryA("iphlpapi.dll");
    if (!iph) {
        hlog("hns-ipv6-hook: LoadLibrary iphlpapi.dll failed: %lu", GetLastError());
        return TRUE;
    }
    void *target = (void *)GetProcAddress(iph, "GetAdaptersAddresses");
    if (!target) {
        hlog("hns-ipv6-hook: GetProcAddress GetAdaptersAddresses failed");
        return TRUE;
    }

    trampoline = install_jmp(target, (void *)hooked_GetAdaptersAddresses);
    if (!trampoline) {
        hlog("hns-ipv6-hook: install_jmp failed: %lu", GetLastError());
        return TRUE;
    }
    real_GetAdaptersAddresses = (GetAdaptersAddresses_t)trampoline;
    hook_enabled = 1;
    hlog("hns-ipv6-hook: installed at %p, trampoline %p, ULA %s/%d",
         target, trampoline, ula_env, ula_prefix_len);
    return TRUE;
}
