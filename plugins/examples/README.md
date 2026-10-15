# Example WASM plugins

Placeholder for milestone **M5** (WebAssembly plugin host).

Planned: one tiny example plugin per language to prove the "any language → `.wasm`"
promise:

- `rust/` — reference plugin (guard / rate rule)
- `csharp/` — plugin for the C# Control Plane story
- `tinygo/` — smallest possible plugin

Each plugin will compile to WASI and be loaded by the Go host via `wazero`.
Nothing here yet — the runtime is not implemented in the MVP.
