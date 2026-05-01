/*
 * probe.exe: a self-test harness for hns-ipv6-hook.dll.
 *
 *   1. Calls GetAdaptersAddresses BEFORE loading the hook DLL and
 *      prints every IPv6 unicast on every adapter.
 *   2. LoadLibrary("C:\\CalicoWindows\\hns-ipv6-hook.dll") so the
 *      DLL's DllMain installs the iphlpapi trampoline in THIS process.
 *   3. Calls GetAdaptersAddresses AGAIN and prints every IPv6 unicast
 *      that survived the filter.
 *
 * This validates the filter end-to-end without touching the live
 * HNS service. Build with:
 *   x86_64-w64-mingw32-gcc -O2 -o probe.exe probe.c -lws2_32 -liphlpapi -static-libgcc
 */

#define WIN32_LEAN_AND_MEAN
#define _WIN32_WINNT 0x0A00
#include <winsock2.h>
#include <ws2tcpip.h>
#include <windows.h>
#include <iphlpapi.h>
#include <stdio.h>

static void dump_v6(const char *label)
{
    ULONG sz = 16 * 1024;
    PIP_ADAPTER_ADDRESSES buf = (PIP_ADAPTER_ADDRESSES)malloc(sz);
    ULONG ret = GetAdaptersAddresses(AF_UNSPEC, 0, NULL, buf, &sz);
    if (ret == ERROR_BUFFER_OVERFLOW) {
        free(buf);
        buf = (PIP_ADAPTER_ADDRESSES)malloc(sz);
        ret = GetAdaptersAddresses(AF_UNSPEC, 0, NULL, buf, &sz);
    }
    if (ret != NO_ERROR) {
        printf("[%s] GetAdaptersAddresses failed: %lu\n", label, ret);
        free(buf);
        return;
    }
    int count = 0;
    for (PIP_ADAPTER_ADDRESSES a = buf; a; a = a->Next) {
        for (PIP_ADAPTER_UNICAST_ADDRESS u = a->FirstUnicastAddress; u; u = u->Next) {
            if (u->Address.lpSockaddr &&
                u->Address.lpSockaddr->sa_family == AF_INET6) {
                struct sockaddr_in6 *sa = (struct sockaddr_in6 *)u->Address.lpSockaddr;
                char str[INET6_ADDRSTRLEN] = {0};
                InetNtopA(AF_INET6, &sa->sin6_addr, str, sizeof(str));
                char ifname[256] = {0};
                WideCharToMultiByte(CP_UTF8, 0, a->FriendlyName, -1,
                                    ifname, sizeof(ifname), NULL, NULL);
                printf("[%s]  %s  %s\n", label, ifname, str);
                count++;
            }
        }
    }
    printf("[%s] total IPv6 unicasts: %d\n", label, count);
    free(buf);
}

int main(void)
{
    printf("== probe.exe == BEFORE hook injection ==\n");
    dump_v6("before");

    HMODULE h = LoadLibraryA("C:\\CalicoWindows\\hns-ipv6-hook.dll");
    if (!h) {
        printf("LoadLibrary failed: %lu\n", GetLastError());
        return 1;
    }
    printf("\n== Hook DLL loaded at %p ==\n\n", h);

    printf("== probe.exe == AFTER hook injection ==\n");
    dump_v6("after");
    return 0;
}
