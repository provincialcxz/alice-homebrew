/* MIT License. Example Alice Homebrew API 3 module without root: the ring
 * follows the playing music, a button press switches the effect and a hand near
 * the proximity sensor dims the ring. No WASI or libc. */
#include "../../sdk/wasm/alice.h"

static u8 frame[36];
static float smooth[12];
static u32 mode;      /* 0 spectrum, 1 pulse */
static float dim = 1; /* eases towards 0.15 while a hand is close */

static unsigned hex(char c) { return c >= '0' && c <= '9' ? c - '0' : c >= 'a' && c <= 'f' ? c - 'a' + 10 : c >= 'A' && c <= 'F' ? c - 'A' + 10 : 0; }

__attribute__((export_name("alice_tick"))) void alice_tick(u32 milliseconds) {
	(void)milliseconds;
	char color[8] = {0};
	alice_config_string("color", 5, color, 7);
	unsigned r = hex(color[1]) * 16 + hex(color[2]), g = hex(color[3]) * 16 + hex(color[4]), b = hex(color[5]) * 16 + hex(color[6]);
	float brightness = (float)(alice_config_number("brightness", 10) / 100.0);
	float near = (float)alice_config_number("near", 4);

	alice_button_event event;
	while (alice_button_event_next(&event))
		if (event.value == 1)
			mode = (mode + 1) % 2;

	double distance = alice_proximity();
	float target = distance >= 0 && distance < near ? 0.15f : 1.0f;
	dim += (target - dim) * 0.2f;

	alice_audio_levels levels;
	u32 playing = alice_audio_levels_read(&levels);
	for (unsigned i = 0; i < 12; i++) {
		float v = !playing ? 0 : mode == 0 ? levels.bands[i] : levels.rms * 2;
		if (v > 1)
			v = 1;
		smooth[i] += (v - smooth[i]) * (v > smooth[i] ? 0.6f : 0.15f); /* fast rise, slow fall */
		float level = smooth[i] * brightness * dim;
		frame[i * 3] = (u8)(b * level);
		frame[i * 3 + 1] = (u8)(g * level);
		frame[i * 3 + 2] = (u8)(r * level);
	}
	alice_led_write(frame, 36);
}
