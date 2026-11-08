- Built on the upstream go-steam and go-dota2 libraries with Go 1.26 or newer, replacing the
  pinned versions whose password login Steam no longer accepts.
- The container image builds with Go 1.27.1 and keeps a writable state volume at
  `/app/state`; the root filesystem stays read-only.
