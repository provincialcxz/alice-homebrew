# Ring Clock

Example plugin for Alice Homebrew API 3 (manager 0.7.0 or newer). It needs only
`led.ring` and uses `utc_time` and `random_u32`, which require no permission.

The bright LED is the hour, the dim one the minutes (in 5-minute steps) and a soft
white light glides around for seconds. LED 0 is twelve o'clock. Set the time zone
in the plugin settings: the speaker reports UTC. Until its clock syncs, a random
LED twinkles.

Source: `plugin.c`, host API: `sdk/wasm/alice.h`. Build with
`scripts/build-example.sh`, then package with
`alice-homebrew pack examples/ring-clock RingClock-1.0.0.aliceplugin`.
