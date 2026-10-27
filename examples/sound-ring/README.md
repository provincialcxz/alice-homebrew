# Sound Ring

Example plugin for Alice Homebrew API 3 (manager 0.7.0 or newer). It runs in the
WebAssembly sandbox, without `system.full-access`:

- `audio.levels`: the ring shows the spectrum (or pulse) of what is playing through
  AirPlay. The microphone is never available to plugins.
- `input.buttons`: any button press switches spectrum and pulse. Alice still gets
  the press, so the volume also moves by one step.
- `sensor.proximity`: a hand closer than the threshold dims the ring. The sensor
  units depend on the driver; adjust the threshold on your speaker.

Source: `plugin.c`, host API: `sdk/wasm/alice.h`. Build with
`scripts/build-example.sh` (clang and wasm-ld), then package with
`alice-homebrew pack examples/sound-ring SoundRing-1.0.0.aliceplugin`.
