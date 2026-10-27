/* Alice Homebrew WebAssembly host API. Plugins export memory and
 * alice_tick(u32 milliseconds); the manager calls it every 50 ms.
 * Calling a function whose permission is not in manifest.json stops the plugin. */
#ifndef ALICE_HOMEBREW_H
#define ALICE_HOMEBREW_H

typedef unsigned char u8;
typedef unsigned short u16;
typedef unsigned int u32;

#define ALICE_IMPORT(name) __attribute__((import_module("alice"), import_name(name)))

/* API 1 and 3: configuration from the manifest's config fields. */
ALICE_IMPORT("config_number") extern double alice_config_number(const char *key, u32 key_length);
ALICE_IMPORT("config_string") extern u32 alice_config_string(const char *key, u32 key_length, char *out, u32 capacity);

/* led.ring: one 36-byte frame per tick, 12 LEDs as B, G, R. Returns 0 on success. */
ALICE_IMPORT("led_write") extern u32 alice_led_write(const u8 *frame, u32 length);

/* log.write: one UTF-8 line up to 1024 bytes. */
ALICE_IMPORT("log") extern void alice_log(const char *text, u32 length);

/* API 3, input.buttons: pops the next key change for this plugin. Returns 1 and
 * fills *event, or 0 when nothing is queued. At most 16 events are kept; a slow
 * plugin loses the oldest. Alice still receives every press. */
typedef struct {
	u16 code;   /* Linux key code reported by adc_keypad */
	u8 value;   /* 1 press, 0 release, 2 repeat */
	u8 reserved;
	u32 millis; /* time since the manager started */
} alice_button_event;
ALICE_IMPORT("button_event") extern u32 alice_button_event_next(alice_button_event *event);

/* API 3, sensor.proximity: last distance reported by the sensor, or -1 before
 * the first report. Units come from the driver; calibrate on your speaker. The
 * manager never enables the sensor, so Alice keeps controlling it. */
ALICE_IMPORT("proximity") extern double alice_proximity(void);

/* API 3, audio.levels: summary of what is playing (currently AirPlay), never the
 * microphone. Fills 14 floats in 0..1: RMS, peak, then 12 frequency bands.
 * Returns 1 while fresh levels exist; otherwise fills zeros and returns 0. */
typedef struct {
	float rms;
	float peak;
	float bands[12];
} alice_audio_levels;
ALICE_IMPORT("audio_levels") extern u32 alice_audio_levels_read(alice_audio_levels *out);

/* API 3, no permission: current UTC time. The speaker's time zone setting is
 * unknown, so add your own offset (for example from a config field). Returns 0
 * while the clock is not synced yet (year before 2024). */
typedef struct {
	u16 year;
	u8 month, day, hour, minute, second;
	u8 weekday; /* 0 Sunday */
} alice_time;
ALICE_IMPORT("utc_time") extern u32 alice_utc_time(alice_time *out);

/* API 3, no permission: a random 32-bit number for effects (not for secrets). */
ALICE_IMPORT("random_u32") extern u32 alice_random_u32(void);

#endif
