# Contributing to sinty-powerd

Thanks for your interest in contributing!

## Quick Start

```bash
git clone https://github.com/singularityos-lab/sinty-powerd
cd sinty-powerd
go build ./...
go test ./...
```

## Guidelines

- Keep changes additive and tested. sinty-powerd runs as root and writes
  power limits; prefer small, verifiable commits over large rewrites.
- What UPower and power-profiles-daemon clients see must not change: compare
  `upower -d` against UPower on the same machine before changing a value.
- Match the surrounding style. Comments explain WHY, not WHAT.
- By submitting a contribution you agree to the [CLA](CLA.md).

## License

GPL-3.0-only.
