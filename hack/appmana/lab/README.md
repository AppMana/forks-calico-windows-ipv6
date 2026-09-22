# Calico Labcontainers tests

This nested Go module keeps AppMana's topology tests independent from Calico's
root module. Install `labd`, build the VM image, then run:

```sh
go run . script-tests
go run . windows-smoke
```

`script-tests` executes the existing AppMana shell/Python suite in a disposable,
network-isolated container. `windows-smoke` requires
`labcontainers/windows-server-2022:latest`; it verifies QGA file transfer,
PowerShell execution, a topology-only NIC, a VirtIO data disk, abrupt power-off,
VM recreation, and disk persistence.

The reusable Windows image and its Packer source live in the Labcontainers
repository. Licensed Windows media and generated qcow2 images are never stored
in this repository.
