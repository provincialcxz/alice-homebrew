// A tiny stand-in for libpulse.so.0. Soloist only plays audio through
// PulseAudio or PipeWire, and the speaker has neither, so this library takes
// the place of the PulseAudio client: the stream Soloist opens is written as
// raw S16LE PCM to the FIFO named by ALICE_PULSE_FIFO, where the plugin
// service reads it and hands it to Alice's mixer. Spotify volume is applied
// here in software. Only the functions Soloist resolves are implemented.
#define _GNU_SOURCE
#include <errno.h>
#include <fcntl.h>
#include <math.h>
#include <pthread.h>
#include <signal.h>
#include <stdarg.h>
#include <stddef.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/time.h>
#include <time.h>
#include <unistd.h>

#define EXPORT __attribute__((visibility("default")))
#define PA_CHANNELS_MAX 32
#define PA_VOLUME_NORM 0x10000U
#define PA_INVALID_INDEX ((uint32_t)-1)

typedef uint64_t pa_usec_t;
typedef uint32_t pa_volume_t;

enum { PA_SAMPLE_S16LE = 3, PA_SAMPLE_FLOAT32LE = 5, PA_SAMPLE_S32LE = 7 };
enum { PA_CONTEXT_UNCONNECTED, PA_CONTEXT_CONNECTING, PA_CONTEXT_AUTHORIZING,
       PA_CONTEXT_SETTING_NAME, PA_CONTEXT_READY, PA_CONTEXT_FAILED,
       PA_CONTEXT_TERMINATED };
enum { PA_STREAM_UNCONNECTED, PA_STREAM_CREATING, PA_STREAM_READY,
       PA_STREAM_FAILED, PA_STREAM_TERMINATED };
enum { PA_OPERATION_RUNNING, PA_OPERATION_DONE, PA_OPERATION_CANCELLED };
enum { PA_SUBSCRIPTION_EVENT_SINK_INPUT = 0x0002,
       PA_SUBSCRIPTION_EVENT_CHANGE = 0x0010 };

typedef struct { int format; uint32_t rate; uint8_t channels; } pa_sample_spec;
typedef struct { uint8_t channels; int map[PA_CHANNELS_MAX]; } pa_channel_map;
typedef struct { uint8_t channels; pa_volume_t values[PA_CHANNELS_MAX]; } pa_cvolume;
typedef struct { uint32_t maxlength, tlength, prebuf, minreq, fragsize; } pa_buffer_attr;

typedef struct {
  struct timeval timestamp;
  int synchronized_clocks;
  pa_usec_t sink_usec, source_usec, transport_usec;
  int playing;
  int write_index_corrupt;
  int64_t write_index;
  int read_index_corrupt;
  int64_t read_index;
  pa_usec_t configured_sink_usec, configured_source_usec;
  int64_t since_underrun;
} pa_timing_info;

typedef struct pa_proplist pa_proplist;

typedef struct {
  uint32_t index;
  const char *name;
  uint32_t owner_module, client, sink;
  pa_sample_spec sample_spec;
  pa_channel_map channel_map;
  pa_cvolume volume;
  pa_usec_t buffer_usec, sink_usec;
  const char *resample_method, *driver;
  int mute;
  pa_proplist *proplist;
  int corked, has_volume, volume_writable;
  void *format;
} pa_sink_input_info;

typedef struct pa_context pa_context;
typedef struct pa_stream pa_stream;
typedef void (*pa_context_notify_cb_t)(pa_context *, void *);
typedef void (*pa_context_success_cb_t)(pa_context *, int, void *);
typedef void (*pa_context_subscribe_cb_t)(pa_context *, int, uint32_t, void *);
typedef void (*pa_sink_input_info_cb_t)(pa_context *, const pa_sink_input_info *, int, void *);
typedef void (*pa_stream_notify_cb_t)(pa_stream *, void *);
typedef void (*pa_stream_success_cb_t)(pa_stream *, int, void *);
typedef void (*pa_free_cb_t)(void *);

// Work handed to the mainloop thread, which runs it with the lock held, the
// same way libpulse delivers every callback.
typedef struct task {
  void (*run)(struct task *);
  void *a, *b, *cb, *userdata;
  int value;
  pa_cvolume volume;
  struct task *next;
} task;

typedef struct {
  pthread_mutex_t lock;    // the pa_threaded_mainloop lock, recursive
  pthread_cond_t cond;     // pa_threaded_mainloop_wait / signal
  pthread_mutex_t qlock;   // task queue
  pthread_cond_t qcond;
  task *head, *tail;
  pthread_t thread;
  int running, stopping;
  char api[256];           // opaque pa_mainloop_api; Soloist never calls into it
} pa_threaded_mainloop;

typedef struct { int state; } pa_operation;

struct pa_context {
  pa_threaded_mainloop *m;
  int state;
  pa_context_notify_cb_t state_cb;
  void *state_ud;
  pa_context_subscribe_cb_t subscribe_cb;
  void *subscribe_ud;
};

struct pa_stream {
  pa_context *c;
  pa_threaded_mainloop *m;
  pa_sample_spec spec;
  int state, corked, started;
  pa_stream_notify_cb_t state_cb, started_cb, underflow_cb;
  void *state_ud, *started_ud, *underflow_ud;
  pa_timing_info timing;
  // Ring buffer of client PCM, guarded by rlock. Positions only grow.
  pthread_mutex_t rlock;
  pthread_cond_t rcond;
  uint8_t *ring;
  size_t cap;
  uint64_t wpos, rpos;
  int closing;
  pthread_t writer;
};

static pa_cvolume g_volume = {2, {PA_VOLUME_NORM, PA_VOLUME_NORM}};
static pthread_mutex_t g_volume_lock = PTHREAD_MUTEX_INITIALIZER;
static int g_fd = -1;
static const uint32_t SINK_INPUT_INDEX = 1;

static void logf_(const char *fmt, ...) __attribute__((format(printf, 1, 2)));
static void logf_(const char *fmt, ...) {
  va_list ap;
  va_start(ap, fmt);
  fputs("alice-pulse: ", stderr);
  vfprintf(stderr, fmt, ap);
  fputc('\n', stderr);
  va_end(ap);
}

static size_t frame_size(const pa_sample_spec *s) {
  size_t sample = s->format == PA_SAMPLE_S16LE ? 2 : 4;
  return sample * (s->channels ? s->channels : 1);
}

static uint64_t now_usec(void) {
  struct timespec ts;
  clock_gettime(CLOCK_MONOTONIC, &ts);
  return (uint64_t)ts.tv_sec * 1000000 + (uint64_t)ts.tv_nsec / 1000;
}

// ---- mainloop -------------------------------------------------------------

static void push(pa_threaded_mainloop *m, task *t) {
  pthread_mutex_lock(&m->qlock);
  t->next = NULL;
  if (m->tail) m->tail->next = t; else m->head = t;
  m->tail = t;
  pthread_cond_signal(&m->qcond);
  pthread_mutex_unlock(&m->qlock);
}

static task *pop(pa_threaded_mainloop *m) {
  pthread_mutex_lock(&m->qlock);
  task *t = m->head;
  if (t) { m->head = t->next; if (!m->head) m->tail = NULL; }
  pthread_mutex_unlock(&m->qlock);
  return t;
}

static void *loop_main(void *arg) {
  pa_threaded_mainloop *m = arg;
  for (;;) {
    pthread_mutex_lock(&m->lock);
    for (task *t; (t = pop(m));) { t->run(t); free(t); }
    int stop = m->stopping;
    pthread_mutex_unlock(&m->lock);
    if (stop) return NULL;
    pthread_mutex_lock(&m->qlock);
    if (!m->head && !m->stopping) {
      struct timespec ts;
      clock_gettime(CLOCK_REALTIME, &ts);
      ts.tv_nsec += 50 * 1000000;
      if (ts.tv_nsec >= 1000000000) { ts.tv_sec++; ts.tv_nsec -= 1000000000; }
      pthread_cond_timedwait(&m->qcond, &m->qlock, &ts);
    }
    pthread_mutex_unlock(&m->qlock);
  }
}

EXPORT pa_threaded_mainloop *pa_threaded_mainloop_new(void) {
  pa_threaded_mainloop *m = calloc(1, sizeof *m);
  if (!m) return NULL;
  pthread_mutexattr_t attr;
  pthread_mutexattr_init(&attr);
  pthread_mutexattr_settype(&attr, PTHREAD_MUTEX_RECURSIVE);
  pthread_mutex_init(&m->lock, &attr);
  pthread_mutexattr_destroy(&attr);
  pthread_cond_init(&m->cond, NULL);
  pthread_mutex_init(&m->qlock, NULL);
  pthread_cond_init(&m->qcond, NULL);
  return m;
}

EXPORT int pa_threaded_mainloop_start(pa_threaded_mainloop *m) {
  if (m->running) return -1;
  m->stopping = 0;
  if (pthread_create(&m->thread, NULL, loop_main, m) != 0) return -1;
  m->running = 1;
  return 0;
}

EXPORT void pa_threaded_mainloop_stop(pa_threaded_mainloop *m) {
  if (!m->running) return;
  pthread_mutex_lock(&m->qlock);
  m->stopping = 1;
  pthread_cond_signal(&m->qcond);
  pthread_mutex_unlock(&m->qlock);
  pthread_join(m->thread, NULL);
  m->running = 0;
}

EXPORT void pa_threaded_mainloop_free(pa_threaded_mainloop *m) {
  if (!m) return;
  pa_threaded_mainloop_stop(m);
  for (task *t; (t = pop(m));) free(t);
  free(m);
}

EXPORT void pa_threaded_mainloop_lock(pa_threaded_mainloop *m) { pthread_mutex_lock(&m->lock); }
EXPORT void pa_threaded_mainloop_unlock(pa_threaded_mainloop *m) { pthread_mutex_unlock(&m->lock); }
EXPORT void pa_threaded_mainloop_wait(pa_threaded_mainloop *m) { pthread_cond_wait(&m->cond, &m->lock); }
EXPORT void pa_threaded_mainloop_signal(pa_threaded_mainloop *m, int wait_for_accept) {
  (void)wait_for_accept;
  pthread_cond_broadcast(&m->cond);
}
EXPORT void *pa_threaded_mainloop_get_api(pa_threaded_mainloop *m) { return m->api; }

// ---- operations -----------------------------------------------------------

static pa_operation *done_op(void) {
  pa_operation *o = calloc(1, sizeof *o);
  if (o) o->state = PA_OPERATION_DONE;
  return o;
}

EXPORT int pa_operation_get_state(const pa_operation *o) { return o ? o->state : PA_OPERATION_DONE; }
EXPORT void pa_operation_unref(pa_operation *o) { free(o); }

static void run_context_success(task *t) {
  if (t->cb) ((pa_context_success_cb_t)t->cb)(t->a, t->value, t->userdata);
}

static void run_stream_success(task *t) {
  if (t->cb) ((pa_stream_success_cb_t)t->cb)(t->a, t->value, t->userdata);
}

static void run_stream_notify(task *t) {
  if (t->cb) ((pa_stream_notify_cb_t)t->cb)(t->a, t->userdata);
}

static pa_operation *later(pa_threaded_mainloop *m, void (*run)(task *), void *a, void *cb, void *ud, int value) {
  if (cb) {
    task *t = calloc(1, sizeof *t);
    if (!t) return NULL;
    t->run = run; t->a = a; t->cb = cb; t->userdata = ud; t->value = value;
    push(m, t);
  }
  return done_op();
}

// ---- context --------------------------------------------------------------

static void set_context_state(task *t) {
  pa_context *c = t->a;
  c->state = t->value;
  if (c->state_cb) c->state_cb(c, c->state_ud);
}

static void context_state_later(pa_context *c, int state) {
  task *t = calloc(1, sizeof *t);
  if (!t) return;
  t->run = set_context_state; t->a = c; t->value = state;
  push(c->m, t);
}

EXPORT pa_context *pa_context_new(void *api, const char *name) {
  (void)name;
  pa_context *c = calloc(1, sizeof *c);
  if (!c) return NULL;
  // The api pointer is the one pa_threaded_mainloop_get_api returned.
  c->m = (pa_threaded_mainloop *)((char *)api - offsetof(pa_threaded_mainloop, api));
  c->state = PA_CONTEXT_UNCONNECTED;
  return c;
}

EXPORT void pa_context_unref(pa_context *c) { free(c); }

EXPORT void pa_context_set_state_callback(pa_context *c, pa_context_notify_cb_t cb, void *ud) {
  c->state_cb = cb; c->state_ud = ud;
}

EXPORT int pa_context_connect(pa_context *c, const char *server, int flags, const void *spawn) {
  (void)server; (void)flags; (void)spawn;
  context_state_later(c, PA_CONTEXT_CONNECTING);
  context_state_later(c, PA_CONTEXT_READY);
  return 0;
}

EXPORT void pa_context_disconnect(pa_context *c) { context_state_later(c, PA_CONTEXT_TERMINATED); }
EXPORT int pa_context_get_state(const pa_context *c) { return c->state; }
EXPORT int pa_context_errno(const pa_context *c) { (void)c; return 0; }

EXPORT void pa_context_set_subscribe_callback(pa_context *c, pa_context_subscribe_cb_t cb, void *ud) {
  c->subscribe_cb = cb; c->subscribe_ud = ud;
}

EXPORT pa_operation *pa_context_subscribe(pa_context *c, int mask, pa_context_success_cb_t cb, void *ud) {
  (void)mask;
  return later(c->m, run_context_success, c, cb, ud, 1);
}

static void run_sink_input_info(task *t) {
  pa_context *c = t->a;
  pa_sink_input_info info;
  memset(&info, 0, sizeof info);
  info.index = SINK_INPUT_INDEX;
  info.name = "Spotify";
  info.owner_module = info.client = info.sink = PA_INVALID_INDEX;
  info.sample_spec = (pa_sample_spec){PA_SAMPLE_S16LE, 44100, 2};
  info.channel_map.channels = 2;
  pthread_mutex_lock(&g_volume_lock);
  info.volume = g_volume;
  pthread_mutex_unlock(&g_volume_lock);
  info.driver = "alice-pulse";
  info.has_volume = info.volume_writable = 1;
  pa_sink_input_info_cb_t cb = (pa_sink_input_info_cb_t)t->cb;
  cb(c, &info, 0, t->userdata);
  cb(c, NULL, 1, t->userdata);
}

EXPORT pa_operation *pa_context_get_sink_input_info(pa_context *c, uint32_t idx, pa_sink_input_info_cb_t cb, void *ud) {
  (void)idx;
  return later(c->m, run_sink_input_info, c, (void *)cb, ud, 0);
}

static void run_volume_event(task *t) {
  pa_context *c = t->a;
  if (c->subscribe_cb)
    c->subscribe_cb(c, PA_SUBSCRIPTION_EVENT_SINK_INPUT | PA_SUBSCRIPTION_EVENT_CHANGE, SINK_INPUT_INDEX, c->subscribe_ud);
}

EXPORT pa_operation *pa_context_set_sink_input_volume(pa_context *c, uint32_t idx, const pa_cvolume *v, pa_context_success_cb_t cb, void *ud) {
  (void)idx;
  if (v) {
    pthread_mutex_lock(&g_volume_lock);
    g_volume = *v;
    pthread_mutex_unlock(&g_volume_lock);
  }
  later(c->m, run_volume_event, c, (void *)1, NULL, 0);
  return later(c->m, run_context_success, c, cb, ud, 1);
}

// ---- volume and time helpers ---------------------------------------------

EXPORT pa_cvolume *pa_cvolume_set(pa_cvolume *a, unsigned channels, pa_volume_t v) {
  if (channels > PA_CHANNELS_MAX) channels = PA_CHANNELS_MAX;
  a->channels = (uint8_t)channels;
  for (unsigned i = 0; i < channels; i++) a->values[i] = v;
  return a;
}

EXPORT pa_volume_t pa_cvolume_avg(const pa_cvolume *a) {
  if (!a || !a->channels) return 0;
  uint64_t sum = 0;
  for (unsigned i = 0; i < a->channels; i++) sum += a->values[i];
  return (pa_volume_t)(sum / a->channels);
}

EXPORT struct timeval *pa_gettimeofday(struct timeval *tv) {
  gettimeofday(tv, NULL);
  return tv;
}

EXPORT pa_usec_t pa_timeval_diff(const struct timeval *a, const struct timeval *b) {
  int64_t ua = (int64_t)a->tv_sec * 1000000 + a->tv_usec;
  int64_t ub = (int64_t)b->tv_sec * 1000000 + b->tv_usec;
  return (pa_usec_t)(ua > ub ? ua - ub : ub - ua);
}

EXPORT size_t pa_usec_to_bytes(pa_usec_t t, const pa_sample_spec *s) {
  return (size_t)(t * s->rate / 1000000) * frame_size(s);
}

EXPORT const char *pa_strerror(int error) { return error ? "alice-pulse error" : "OK"; }

// Soloist only fills a proplist to name its stream; nothing reads it back.
EXPORT pa_proplist *pa_proplist_new(void) { return calloc(1, 1); }
EXPORT void pa_proplist_free(pa_proplist *p) { free(p); }
EXPORT int pa_proplist_sets(pa_proplist *p, const char *key, const char *value) {
  (void)p; (void)key; (void)value;
  return 0;
}

// ---- output ---------------------------------------------------------------

static int open_output(void) {
  if (g_fd >= 0) return g_fd;
  const char *path = getenv("ALICE_PULSE_FIFO");
  if (!path || !*path) path = "/dev/null";
  g_fd = open(path, O_WRONLY | O_CLOEXEC);
  if (g_fd < 0) logf_("open %s: %s", path, strerror(errno));
  return g_fd;
}

static float gain_now(void) {
  pthread_mutex_lock(&g_volume_lock);
  pa_volume_t v = pa_cvolume_avg(&g_volume);
  pthread_mutex_unlock(&g_volume_lock);
  if (v >= PA_VOLUME_NORM) return 1.0f;
  float x = (float)v / PA_VOLUME_NORM;
  return x * x * x;  // PulseAudio software volume is cubic
}

static int16_t clamp16(float v) {
  if (v > 32767.0f) return 32767;
  if (v < -32768.0f) return -32768;
  return (int16_t)lrintf(v);
}

// Converts n bytes of client PCM to S16LE with the current gain applied.
static size_t convert(const pa_sample_spec *s, const uint8_t *in, size_t n, int16_t *out) {
  float g = gain_now();
  size_t count = 0;
  if (s->format == PA_SAMPLE_S16LE) {
    for (size_t i = 0; i + 1 < n; i += 2) {
      int16_t v; memcpy(&v, in + i, 2);
      out[count++] = g == 1.0f ? v : clamp16(v * g);
    }
  } else if (s->format == PA_SAMPLE_FLOAT32LE) {
    for (size_t i = 0; i + 3 < n; i += 4) {
      float v; memcpy(&v, in + i, 4);
      out[count++] = clamp16(v * g * 32768.0f);
    }
  } else {  // S32LE
    for (size_t i = 0; i + 3 < n; i += 4) {
      int32_t v; memcpy(&v, in + i, 4);
      out[count++] = clamp16((float)v / 65536.0f * g);
    }
  }
  return count * 2;
}

static int write_all(int fd, const void *buf, size_t n) {
  const uint8_t *p = buf;
  while (n) {
    ssize_t w = write(fd, p, n);
    if (w < 0) { if (errno == EINTR) continue; return -1; }
    p += w; n -= (size_t)w;
  }
  return 0;
}

static void *writer_main(void *arg) {
  pa_stream *s = arg;
  enum { CHUNK = 4096 };
  uint8_t in[CHUNK];
  int16_t out[CHUNK];
  int pace = getenv("ALICE_PULSE_PACE") != NULL;
  uint64_t start = 0, played = 0;
  size_t fsize = frame_size(&s->spec);
  for (;;) {
    pthread_mutex_lock(&s->rlock);
    while (!s->closing && (s->corked || s->wpos == s->rpos)) {
      start = 0;
      pthread_cond_wait(&s->rcond, &s->rlock);
    }
    if (s->closing) { pthread_mutex_unlock(&s->rlock); return NULL; }
    size_t n = (size_t)(s->wpos - s->rpos);
    if (n > CHUNK) n = CHUNK;
    n -= n % fsize;
    if (!n) n = (size_t)(s->wpos - s->rpos) < CHUNK ? (size_t)(s->wpos - s->rpos) : CHUNK;
    for (size_t i = 0; i < n; i++) in[i] = s->ring[(s->rpos + i) % s->cap];
    pthread_mutex_unlock(&s->rlock);

    size_t bytes = convert(&s->spec, in, n, out);
    int fd = open_output();
    if (fd >= 0 && write_all(fd, out, bytes) < 0) {
      logf_("write: %s", strerror(errno));
      close(fd);
      g_fd = -1;
    }
    if (pace) {  // for tests without a real-time consumer
      if (!start) { start = now_usec(); played = 0; }
      played += n / fsize;
      uint64_t due = start + played * 1000000 / s->spec.rate;
      uint64_t now = now_usec();
      if (due > now) usleep((useconds_t)(due - now));
    }

    pthread_mutex_lock(&s->rlock);
    s->rpos += n;
    int first = !s->started;
    s->started = 1;
    int empty = s->wpos == s->rpos;
    pthread_mutex_unlock(&s->rlock);
    if (first && s->started_cb) later(s->m, run_stream_notify, s, (void *)s->started_cb, s->started_ud, 0);
    if (empty && s->underflow_cb) later(s->m, run_stream_notify, s, (void *)s->underflow_cb, s->underflow_ud, 0);
  }
}

// ---- stream ---------------------------------------------------------------

static void set_stream_state(task *t) {
  pa_stream *s = t->a;
  s->state = t->value;
  if (s->state_cb) s->state_cb(s, s->state_ud);
}

static void stream_state_later(pa_stream *s, int state) {
  task *t = calloc(1, sizeof *t);
  if (!t) return;
  t->run = set_stream_state; t->a = s; t->value = state;
  push(s->m, t);
}

EXPORT pa_stream *pa_stream_new_with_proplist(pa_context *c, const char *name, const pa_sample_spec *ss, const pa_channel_map *map, pa_proplist *p) {
  (void)name; (void)map; (void)p;
  if (ss->format != PA_SAMPLE_S16LE && ss->format != PA_SAMPLE_FLOAT32LE && ss->format != PA_SAMPLE_S32LE) {
    logf_("unsupported sample format %d", ss->format);
    return NULL;
  }
  pa_stream *s = calloc(1, sizeof *s);
  if (!s) return NULL;
  s->c = c; s->m = c->m; s->spec = *ss;
  pthread_mutex_init(&s->rlock, NULL);
  pthread_cond_init(&s->rcond, NULL);
  logf_("stream format=%d rate=%u channels=%u", ss->format, ss->rate, ss->channels);
  return s;
}

EXPORT pa_stream *pa_stream_new(pa_context *c, const char *name, const pa_sample_spec *ss, const pa_channel_map *map) {
  return pa_stream_new_with_proplist(c, name, ss, map, NULL);
}

EXPORT int pa_stream_connect_playback(pa_stream *s, const char *dev, const pa_buffer_attr *attr, int flags, const pa_cvolume *volume, pa_stream *sync) {
  (void)dev; (void)flags; (void)sync;
  size_t second = s->spec.rate * frame_size(&s->spec);
  size_t cap = attr && attr->tlength && attr->tlength != (uint32_t)-1 ? attr->tlength : second / 4;
  if (cap < second / 20) cap = second / 20;
  if (cap > second * 2) cap = second * 2;
  s->ring = malloc(cap);
  if (!s->ring) return -1;
  s->cap = cap;
  s->corked = (flags & 0x0001) != 0;  // PA_STREAM_START_CORKED
  if (volume) {
    pthread_mutex_lock(&g_volume_lock);
    g_volume = *volume;
    pthread_mutex_unlock(&g_volume_lock);
  }
  if (pthread_create(&s->writer, NULL, writer_main, s) != 0) return -1;
  stream_state_later(s, PA_STREAM_CREATING);
  stream_state_later(s, PA_STREAM_READY);
  return 0;
}

static void stop_writer(pa_stream *s) {
  if (!s->ring) return;
  pthread_mutex_lock(&s->rlock);
  s->closing = 1;
  pthread_cond_broadcast(&s->rcond);
  pthread_mutex_unlock(&s->rlock);
  // The writer may be blocked in write(); a mutex-holding mainloop caller
  // must not wait for it, so it is detached and frees nothing it shares.
  pthread_detach(s->writer);
}

EXPORT int pa_stream_disconnect(pa_stream *s) {
  stop_writer(s);
  stream_state_later(s, PA_STREAM_TERMINATED);
  return 0;
}

EXPORT void pa_stream_unref(pa_stream *s) {
  // The detached writer may still touch the stream, so it is leaked on
  // purpose: Soloist opens a handful of streams per session at most.
  (void)s;
}

EXPORT int pa_stream_get_state(const pa_stream *s) { return s->state; }
EXPORT uint32_t pa_stream_get_index(const pa_stream *s) { (void)s; return SINK_INPUT_INDEX; }

EXPORT void pa_stream_set_state_callback(pa_stream *s, pa_stream_notify_cb_t cb, void *ud) { s->state_cb = cb; s->state_ud = ud; }
EXPORT void pa_stream_set_started_callback(pa_stream *s, pa_stream_notify_cb_t cb, void *ud) { s->started_cb = cb; s->started_ud = ud; }
EXPORT void pa_stream_set_underflow_callback(pa_stream *s, pa_stream_notify_cb_t cb, void *ud) { s->underflow_cb = cb; s->underflow_ud = ud; }

EXPORT size_t pa_stream_writable_size(const pa_stream *s) {
  pa_stream *w = (pa_stream *)s;
  pthread_mutex_lock(&w->rlock);
  size_t free_bytes = w->cap - (size_t)(w->wpos - w->rpos);
  pthread_mutex_unlock(&w->rlock);
  return free_bytes;
}

EXPORT int pa_stream_write(pa_stream *s, const void *data, size_t nbytes, pa_free_cb_t free_cb, int64_t offset, int seek) {
  (void)offset; (void)seek;
  const uint8_t *p = data;
  pthread_mutex_lock(&s->rlock);
  size_t room = s->cap - (size_t)(s->wpos - s->rpos);
  if (nbytes > room) nbytes = room;  // the client asked writable_size first
  for (size_t i = 0; i < nbytes; i++) s->ring[(s->wpos + i) % s->cap] = p[i];
  s->wpos += nbytes;
  pthread_cond_broadcast(&s->rcond);
  pthread_mutex_unlock(&s->rlock);
  if (free_cb) free_cb((void *)data);
  return 0;
}

EXPORT pa_operation *pa_stream_cork(pa_stream *s, int b, pa_stream_success_cb_t cb, void *ud) {
  pthread_mutex_lock(&s->rlock);
  s->corked = b;
  pthread_cond_broadcast(&s->rcond);
  pthread_mutex_unlock(&s->rlock);
  return later(s->m, run_stream_success, s, (void *)cb, ud, 1);
}

EXPORT int pa_stream_is_corked(const pa_stream *s) { return s->corked; }

EXPORT pa_operation *pa_stream_flush(pa_stream *s, pa_stream_success_cb_t cb, void *ud) {
  pthread_mutex_lock(&s->rlock);
  s->rpos = s->wpos;
  pthread_mutex_unlock(&s->rlock);
  return later(s->m, run_stream_success, s, (void *)cb, ud, 1);
}

EXPORT pa_operation *pa_stream_update_timing_info(pa_stream *s, pa_stream_success_cb_t cb, void *ud) {
  pthread_mutex_lock(&s->rlock);
  pa_timing_info *t = &s->timing;
  gettimeofday(&t->timestamp, NULL);
  t->synchronized_clocks = 1;
  size_t fsize = frame_size(&s->spec);
  t->sink_usec = (pa_usec_t)((s->wpos - s->rpos) / fsize) * 1000000 / s->spec.rate;
  t->playing = !s->corked && s->wpos != s->rpos;
  t->write_index = (int64_t)s->wpos;
  t->read_index = (int64_t)s->rpos;
  pthread_mutex_unlock(&s->rlock);
  return later(s->m, run_stream_success, s, (void *)cb, ud, 1);
}

EXPORT const pa_timing_info *pa_stream_get_timing_info(pa_stream *s) { return &s->timing; }

__attribute__((constructor)) static void init(void) {
  // A reader that goes away must not kill Soloist with SIGPIPE.
  signal(SIGPIPE, SIG_IGN);
}
