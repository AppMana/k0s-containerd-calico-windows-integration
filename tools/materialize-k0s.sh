#!/usr/bin/env bash
# Reconstruct the tested installer commits on the pinned upstream submodule.
# No network fetch, branch creation, reset, or modification of dirty sources.
set -euo pipefail
[[ $# == 1 ]] || { echo 'usage: materialize-k0s.sh CLEAN_K0S_CHECKOUT' >&2; exit 2; }
target=$(realpath "$1")
root=$(git -C "$(dirname "$0")" rev-parse --show-toplevel)
bundle="$root/bundles/k0s-13893f0.bundle"
base=bdf1c22c23a5af23ec6a1ff764179f6a6cf6f7dc
candidate=13893f0ab766ab03eafecaa4807ce6bf3bc59668
tree=3fbf5bcdf7276d21f218fb2e1e77fac5bee6b245
[[ $(git -C "$target" rev-parse --show-toplevel) == "$target" ]] || { echo 'target must be the checkout root' >&2; exit 1; }
[[ -z $(git -C "$target" status --porcelain) ]] || { echo 'refusing dirty source checkout' >&2; exit 1; }
current=$(git -C "$target" rev-parse HEAD)
[[ "$current" == "$base" || "$current" == "$candidate" ]] || { echo 'unexpected source revision' >&2; exit 1; }
[[ $(sha256sum "$bundle" | cut -d' ' -f1) == 5a4a01fa9b396c69e953bb91ce404a3bfc2c4b29bbdfbc6e7b5ea22b0d6dc052 ]] || { echo 'source bundle digest mismatch' >&2; exit 1; }
git -C "$target" bundle verify "$bundle"
git -C "$target" fetch --no-tags "$bundle" HEAD
[[ $(git -C "$target" rev-parse FETCH_HEAD) == "$candidate" ]] || { echo 'bundle ref mismatch' >&2; exit 1; }
[[ $(git -C "$target" rev-parse "$candidate^{tree}") == "$tree" ]] || { echo 'source tree mismatch' >&2; exit 1; }
git -C "$target" checkout --detach "$candidate"
printf 'K0S_SOURCE_READY:%s tree=%s\n' "$candidate" "$tree"
