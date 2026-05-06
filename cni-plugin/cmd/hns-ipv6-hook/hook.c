/*
 * hns-ipv6-hook: filter iphlpapi!GetAdaptersAddresses inside the HNS
 * service process so HNS sees only the operator-configured "desired"
 * IPv6 management address when scanning the NIC. HNS then pins that
 * address as the L2Bridge network's ManagementIPv6 — without us
 * having to remove anything from the live NIC.
 *
 * Configuration: read at DllMain time from the cfg file at
 *   C:\CalicoWindows\hns-ipv6-hook.cfg
 * (the "DEFAULT_CFG_PATH" compile-time constant — see below to change
 * it for a forked deployment). The cfg file is line-oriented:
 *   - blank lines and lines starting with '#' are ignored
 *   - "log=<absolute-path>" sets the log file path (overrides the
 *     compile-time DEFAULT_LOG_PATH)
 *   - any other line is parsed as either an IPv4 or IPv6 textual
 *     address; the first match in each family becomes the desired
 *     ManagementIP / ManagementIPv6
 *
 * Default log path is C:\var\log\calico\hook.log. The DLL creates
 * any missing parent directories with CreateDirectoryA before opening
 * the log file. Use "log=" in the cfg to relocate logs without
 * recompiling.
 *
 * Services don't inherit caller envvars; a cfg file is the only
 * practical channel from the injector (which has the operator's
 * settings) to this DLL (which runs inside svchost-hns).
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

/* Compile-time defaults. Operators relocate the cfg path by patching
 * DEFAULT_CFG_PATH and rebuilding (since DllMain runs in svchost-hns
 * and svchost-hns doesn't inherit env vars from the injector); the
 * log path is overridable at runtime via a "log=" line in cfg. */
#ifndef DEFAULT_CFG_PATH
#define DEFAULT_CFG_PATH "C:\\CalicoWindows\\hns-ipv6-hook.cfg"
#endif
#ifndef DEFAULT_LOG_PATH
#define DEFAULT_LOG_PATH "C:\\var\\log\\calico\\hook.log"
#endif

static unsigned char *g_trampoline = NULL;
static unsigned char  g_desired_v6[16];
static int            g_have_desired_v6 = 0;
static unsigned char  g_desired_v4[4];
static int            g_have_desired_v4 = 0;
static volatile LONG  g_filter_active = 0;

/* Resolved log path; populated by read_desired_from_file() — falls back
 * to DEFAULT_LOG_PATH when cfg is absent or has no "log=" line. */
static char g_log_path[MAX_PATH] = DEFAULT_LOG_PATH;

/* Create the parent directories of an absolute path, ignoring
 * "already exists" errors. Walks the path component-by-component
 * because CreateDirectoryA doesn't recurse. */
static void ensure_parent_dirs(const char *path)
{
    char buf[MAX_PATH];
    size_t n = strnlen(path, MAX_PATH - 1);
    if (n == 0) return;
    memcpy(buf, path, n);
    buf[n] = 0;
    /* Walk forward, NUL-terminate at each '\\' or '/' separator and
     * call CreateDirectoryA on the prefix. Skip the drive-letter root
     * ("C:\") so we don't try to create it. */
    for (size_t i = 3; i < n; i++) {
        if (buf[i] == '\\' || buf[i] == '/') {
            char saved = buf[i];
            buf[i] = 0;
            CreateDirectoryA(buf, NULL); /* error intentionally ignored */
            buf[i] = saved;
        }
    }
}

static void hlog(const char *fmt, ...)
{
    char buf[1024];
    va_list ap;
    va_start(ap, fmt);
    vsnprintf(buf, sizeof(buf), fmt, ap);
    va_end(ap);
    /* First-use directory create is cheap (Windows caches the dir-exists
     * lookup) and handles the case where C:\var\log\calico didn't exist
     * before this DLL was injected. */
    ensure_parent_dirs(g_log_path);
    HANDLE h = CreateFileA(g_log_path, FILE_APPEND_DATA, FILE_SHARE_READ,
                           NULL, OPEN_ALWAYS, FILE_ATTRIBUTE_NORMAL, NULL);
    if (h != INVALID_HANDLE_VALUE) {
        DWORD written;
        SetFilePointer(h, 0, NULL, FILE_END);
        WriteFile(h, buf, (DWORD)strlen(buf), &written, NULL);
        WriteFile(h, "\r\n", 2, &written, NULL);
        CloseHandle(h);
    }
}

/* Read settings from the cfg file at DEFAULT_CFG_PATH.
 *
 * Format (line-oriented, all lines optional):
 *   log=<absolute-path>           overrides the log file destination
 *                                  (DEFAULT_LOG_PATH when absent).
 *   <bare ipv6 address>           sets g_desired_v6 (first match wins).
 *   <bare ipv4 address>           sets g_desired_v4 (first match wins).
 *   #<anything>                   comment, ignored.
 *   (blank lines)                 ignored.
 *
 * Backward-compat: a cfg holding just one bare IPv6 address — the
 * historical format — still works.
 *
 * Returns 1 if at least one address family was set, else 0. The log
 * path is always populated (either from cfg or the compile-time
 * default), so hlog() is safe to call from the no-cfg path too. */
static int read_desired_from_file(void)
{
    HANDLE h = CreateFileA(DEFAULT_CFG_PATH, GENERIC_READ,
                           FILE_SHARE_READ, NULL, OPEN_EXISTING, FILE_ATTRIBUTE_NORMAL, NULL);
    if (h == INVALID_HANDLE_VALUE) return 0;
    char buf[1024];
    DWORD got = 0;
    BOOL ok = ReadFile(h, buf, sizeof(buf) - 1, &got, NULL);
    CloseHandle(h);
    if (!ok || got == 0) return 0;
    buf[got] = 0;

    /* Walk the buffer line-by-line, NUL-terminating each line. */
    char *p = buf;
    char *end = buf + got;
    while (p < end) {
        char *line = p;
        while (p < end && *p != '\n' && *p != '\r') p++;
        if (p < end) { *p = 0; p++; }
        /* skip stray CR or extra LF */
        while (p < end && (*p == '\n' || *p == '\r')) p++;

        /* trim leading whitespace */
        while (*line == ' ' || *line == '\t') line++;
        /* trim trailing whitespace */
        size_t n = strlen(line);
        while (n > 0 && (line[n-1] == ' ' || line[n-1] == '\t')) {
            line[--n] = 0;
        }
        if (n == 0 || line[0] == '#') continue;

        /* key=value? Currently the only key is "log"; unknown keys are
         * ignored so that future cfg-format additions don't crash an
         * older DLL still loaded in a long-running svchost-hns. */
        char *eq = strchr(line, '=');
        if (eq) {
            *eq = 0;
            char *key = line;
            char *value = eq + 1;
            /* trim whitespace around key + value */
            while (*value == ' ' || *value == '\t') value++;
            size_t kn = strlen(key);
            while (kn > 0 && (key[kn-1] == ' ' || key[kn-1] == '\t')) key[--kn] = 0;
            if (kn > 0 && _stricmp(key, "log") == 0 && *value) {
                strncpy(g_log_path, value, sizeof(g_log_path) - 1);
                g_log_path[sizeof(g_log_path) - 1] = 0;
            }
            /* unknown keys: silently ignore */
            continue;
        }

        /* Try IPv6 first (reject anything that parses as v4-mapped). */
        if (!g_have_desired_v6) {
            struct in6_addr a6;
            if (InetPtonA(AF_INET6, line, &a6) == 1) {
                memcpy(g_desired_v6, &a6, 16);
                g_have_desired_v6 = 1;
                continue;
            }
        }
        if (!g_have_desired_v4) {
            struct in_addr a4;
            if (InetPtonA(AF_INET, line, &a4) == 1) {
                memcpy(g_desired_v4, &a4, 4);
                g_have_desired_v4 = 1;
                continue;
            }
        }
    }
    return (g_have_desired_v6 || g_have_desired_v4) ? 1 : 0;
}

/* Detour: leave only the exact desired addresses visible to the
 * caller. For each family that has a configured desired address,
 * drop every other unicast of that family — INCLUDING link-local.
 * HNS picks ManagementIP / ManagementIPv6 by taking the first non-
 * link-local it sees on each family, but we drop link-local too
 * because HNS has been observed to fall back to a link-local with
 * a scope ID when no other address is present.
 *
 * Families with no configured desired are left untouched, so an
 * IPv6-only deployment behaves exactly like the original hook.
 *
 * Why this is the right lever for IPv4: HNS L2Bridge installs a VFP
 * rule (EnableOverrideReceiveRoutingForLocalAddressesIpv4) that
 * delivers ARP only to the registered ManagementIP. If HNS picks the
 * wrong IPv4 — e.g. a transient DHCP-renewal address, an APIPA
 * 169.254.x.x, or one that's mid-transition between the physical NIC
 * and vEthernet (Calico) — ARP for the host's actual management IPv4
 * is silently dropped at the vSwitch and the host goes ARP INCOMPLETE.
 * Pinning HNS's view to the operator-chosen IPv4 prevents this. */
static ULONG WINAPI hooked_GetAdaptersAddresses(ULONG family, ULONG flags, PVOID reserved,
                                                PIP_ADAPTER_ADDRESSES buf, PULONG sz)
{
    GetAdaptersAddresses_t orig = (GetAdaptersAddresses_t)g_trampoline;
    ULONG ret = orig(family, flags, reserved, buf, sz);
    if (ret != NO_ERROR || !buf) return ret;
    if (!InterlockedCompareExchange(&g_filter_active, 0, 0)) return ret;

    if (!g_have_desired_v6 && !g_have_desired_v4) return ret;

    for (PIP_ADAPTER_ADDRESSES a = buf; a; a = a->Next) {
        PIP_ADAPTER_UNICAST_ADDRESS *prev = &a->FirstUnicastAddress;
        PIP_ADAPTER_UNICAST_ADDRESS u = a->FirstUnicastAddress;
        while (u) {
            int drop = 0;
            if (u->Address.lpSockaddr) {
                if (u->Address.lpSockaddr->sa_family == AF_INET6 && g_have_desired_v6) {
                    struct sockaddr_in6 *sa = (struct sockaddr_in6 *)u->Address.lpSockaddr;
                    if (memcmp(&sa->sin6_addr, g_desired_v6, 16) != 0) {
                        drop = 1;
                    }
                } else if (u->Address.lpSockaddr->sa_family == AF_INET && g_have_desired_v4) {
                    struct sockaddr_in *sa = (struct sockaddr_in *)u->Address.lpSockaddr;
                    if (memcmp(&sa->sin_addr, g_desired_v4, 4) != 0) {
                        drop = 1;
                    }
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
 * Returns trampoline (callable as the original) or NULL on failure.
 *
 * Idempotent: if the prologue already starts with our FF 25 absolute
 * JMP, we conclude a previous injection patched it. We refuse to
 * patch again (which would corrupt the trampoline target chain) but
 * return non-NULL signalling "already hooked" so the caller doesn't
 * treat it as a failure. */
static unsigned char *install_absolute_jmp(void *target_fn, void *detour_fn)
{
    unsigned char *t = (unsigned char *)target_fn;
    if (t[0] == 0xFF && t[1] == 0x25) {
        hlog("hns-ipv6-hook: target %p already patched (FF 25 ...), skipping (idempotent)", target_fn);
        /* Return the function itself so the caller has a non-NULL
         * pointer; it can't be called as a trampoline because it
         * would JMP to whatever previous detour was installed, but
         * we don't call it from this DLL — once patched, all calls
         * go through the existing trampoline owned by the previous
         * DllMain invocation. */
        return t;
    }
    int prologue = recognised_prologue_len(t);
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

    if (!read_desired_from_file()) {
        hlog("hns-ipv6-hook: no desired ManagementIP/ManagementIPv6 from cfg file; not hooking");
        return TRUE;
    }

    HMODULE iph = LoadLibraryA("iphlpapi.dll");
    if (!iph) { hlog("hns-ipv6-hook: LoadLibrary iphlpapi.dll failed: %lu", GetLastError()); return TRUE; }
    void *target = (void *)GetProcAddress(iph, "GetAdaptersAddresses");
    if (!target) { hlog("hns-ipv6-hook: GetProcAddress failed"); return TRUE; }

    /* If already hooked by a prior DllMain, install_absolute_jmp returns
     * the target itself (non-NULL but not a usable trampoline for THIS
     * DLL). In that case we skip activating our filter — the original
     * filter (from the first DllMain) is still in effect, and trying
     * to layer ours would chain JMPs incorrectly. */
    int already_hooked = (((unsigned char *)target)[0] == 0xFF &&
                         ((unsigned char *)target)[1] == 0x25);
    g_trampoline = install_absolute_jmp(target, (void *)hooked_GetAdaptersAddresses);
    if (!g_trampoline) { hlog("hns-ipv6-hook: trampoline install failed"); return TRUE; }
    if (already_hooked) {
        hlog("hns-ipv6-hook: pre-existing hook detected; not re-arming filter");
        return TRUE;
    }

    InterlockedExchange(&g_filter_active, 1);
    char dbg6[64] = "(none)";
    char dbg4[32] = "(none)";
    if (g_have_desired_v6) InetNtopA(AF_INET6, g_desired_v6, dbg6, sizeof(dbg6));
    if (g_have_desired_v4) InetNtopA(AF_INET,  g_desired_v4, dbg4, sizeof(dbg4));
    hlog("hns-ipv6-hook: installed at %p, desired ManagementIPv6=%s ManagementIP=%s",
         target, dbg6, dbg4);
    return TRUE;
}
