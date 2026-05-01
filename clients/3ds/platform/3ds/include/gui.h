#pragma once
#include <3ds.h>
#include <citro2d.h>
#include <stdint.h>
#include <stdbool.h>

// ---------- Color constants (ABGR8888) ----------
#define GUI_BLACK       0xFF000000
#define GUI_WHITE       0xFFFFFFFF
#define GUI_RED         0xFF0000FF
#define GUI_GREEN       0xFF00FF00
#define GUI_BLUE        0xFFFF0000
#define GUI_YELLOW      0xFF00C5FF
#define GUI_GRAY        0xFF777777
#define GUI_DARK_GRAY   0xFF333333
#define GUI_LIGHT_GRAY  0xFFAAAAAA
#define GUI_NO_COLOR    0x00000000

// Semi-transparent colors for overlays
#define GUI_TRANS_BLACK 0xD0000000
#define GUI_TRANS_WHITE 0xD0FFFFFF

// ---------- Layout ----------
#define GUI_TOP_BAR_H    14.0f
#define GUI_BOT_BAR_H    14.0f
#define GUI_MARGIN       4.0f

// ---------- GUI state ----------
typedef struct {
    bool show_overlay;      // toggled with X
    bool show_debug;        // toggled with Y
    int  fps;               // calculated each second
    int  frame_count;       // frames this second
    u64  last_fps_time;     // osGetTime() at last fps calc

    // Status strings (updated each frame)
    char status_line[64];   // e.g. "Connected  |  192.168.1.42:8001"
    char stream_info[64];   // e.g. "400x240  |  30 FPS"
    char debug_line[64];    // e.g. "Frames: 1234  Dropped: 0"

    // UDP diagnostics
    uint32_t udp_calls;
    uint32_t udp_bytes;
    uint32_t udp_packets;
    uint32_t udp_errors;

    // Frame pipeline counters
    uint32_t nal_frames;
    uint32_t decoded_frames;
    uint32_t displayed_frames;
    uint32_t dropped_frames;
} Gui_state;

// ---------- Lifecycle ----------
bool gui_init(void);
void gui_deinit(void);

// Call once per frame to update FPS counter and status strings.
// Pass connection status (true = streaming), IP string, port.
void gui_update_frame(bool connected, const char *ip, int port);

// Draw the GUI overlay on top of the video.
// Call after drawing the video texture but before C3D_FrameEnd.
void gui_draw(void);

// Query/Set overlay visibility
bool gui_overlay_visible(void);
void gui_set_overlay_visible(bool visible);

// Query/Set debug panel visibility
bool gui_debug_visible(void);
void gui_set_debug_visible(bool visible);

// Convenience: draw text at (x,y) with scale and color
void gui_draw_text(const char *text, float x, float y, float scale, u32 color);

// Draw a filled rectangle
void gui_draw_rect(float x, float y, float w, float h, u32 color);

// Draw a large centered status message (for showing IP / connection info)
void gui_draw_big_status(const char *line1, const char *line2, u32 color);

// Update UDP diagnostic counters shown in debug panel
void gui_set_udp_stats(uint32_t calls, uint32_t bytes, uint32_t packets, uint32_t errors);

// Update frame decode counters
void gui_set_frame_stats(uint32_t nal_frames, uint32_t decoded_frames, uint32_t displayed_frames, uint32_t dropped_frames);
