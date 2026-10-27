/* MIT License. Example Alice Homebrew API 3 module: a clock on the 12-LED ring.
 * The hour LED is bright, the minute LED is dimmer, and the second LED glides
 * around the ring. Before the clock syncs, a random LED twinkles. No WASI or libc. */
#include "../../sdk/wasm/alice.h"

static u8 frame[36];

static unsigned hex(char c) { return c >= '0' && c <= '9' ? c - '0' : c >= 'a' && c <= 'f' ? c - 'a' + 10 : c >= 'A' && c <= 'F' ? c - 'A' + 10 : 0; }

static void add(unsigned led, unsigned r, unsigned g, unsigned b, float level) {
	u8 *p = frame + (led % 12) * 3;
	unsigned v[3] = {p[0] + (unsigned)(b * level), p[1] + (unsigned)(g * level), p[2] + (unsigned)(r * level)};
	for (int i = 0; i < 3; i++)
		p[i] = v[i] > 255 ? 255 : (u8)v[i];
}

__attribute__((export_name("alice_tick"))) void alice_tick(u32 milliseconds) {
	char color[8] = {0};
	alice_config_string("color", 5, color, 7);
	unsigned r = hex(color[1]) * 16 + hex(color[2]), g = hex(color[3]) * 16 + hex(color[4]), b = hex(color[5]) * 16 + hex(color[6]);
	float brightness = (float)(alice_config_number("brightness", 10) / 100.0);
	int offset = (int)alice_config_number("utc-offset", 10);
	u32 seconds_hand = alice_config_number("seconds", 7) > 0;
	for (unsigned i = 0; i < 36; i++)
		frame[i] = 0;

	alice_time now;
	if (!alice_utc_time(&now)) {
		if (milliseconds % 400 < 50) /* twinkle until the clock syncs */
			add(alice_random_u32() % 12, r, g, b, brightness * 0.3f);
		alice_led_write(frame, 36);
		return;
	}
	int hour = ((int)now.hour + offset % 24 + 24) % 24;
	/* LED 0 is twelve o'clock, as on a watch face. */
	add(hour % 12, r, g, b, brightness);
	add(now.minute / 5, r, g, b, brightness * 0.35f);
	if (seconds_hand) {
		float pos = now.second / 5.0f;
		unsigned led = (unsigned)pos;
		float frac = pos - led;
		add(led, 255, 255, 255, brightness * 0.12f * (1 - frac));
		add(led + 1, 255, 255, 255, brightness * 0.12f * frac);
	}
	alice_led_write(frame, 36);
}
