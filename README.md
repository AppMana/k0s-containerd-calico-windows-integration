# k0s / containerd / Calico integration

Pinned component sources and Go test entrypoints for AppMana's labcontainers-based Linux/Windows qualification. Component boundary tests are separate from live VM qualification; neither proves IPv6 prefix-rotation sandbox replacement yet.

`go test -race ./cmd/... ./internal/...` runs the shutdown-probe and replacement-evidence contracts.

For native Windows component execution, use `go -C lab test -v -count=1 -run '^TestWindowsRuntimeCandidate$' -timeout=17m .` with:

- `INTEGRATION_WINDOWS_RUNTIME=1`
- `LABCONTAINERS_WINDOWS_IMAGE`, `LABCONTAINERS_LABD`, and an absolute durable `LABCONTAINERS_STATE_DIR`. The image helper and daemon must match the SDK pin in `lab/go.mod`.
- `INTEGRATION_RUNTIME_MEDIA` and `INTEGRATION_RUNTIME_MEDIA_SHA256`: an ISO containing the output directory from containerd's `script/build-windows-candidate`, preserving its directory structure and `SHA256SUMS`. Create it using `xorriso -as mkisofs -J -R -o /absolute/runtime.iso /absolute/candidate`.
- `INTEGRATION_RUNTIME_VERSION` and `INTEGRATION_RUNTIME_REVISION`: the expected executable build identity from the candidate provenance, not the integration repository's revision.

The fixture provisions a private Windows VM with no NIC and a read-only artifact disk, checks hashes, executes the native CRI CHECK/status tests, and destroys its owned VM even on failure. It retains Labcontainers artifacts. This gate does **not** qualify live CNI prefix rotation, graceful sandbox replacement, or upgrading an existing node.
