#include "gui.h"
#include "logger.h"
#include <string.h>
#include <stdio.h>

// citro2d text buffer (static so we don't realloc every frame)
static C2D_TextBuf s_text_buf = NULL;
static C2D_Text   s_text_obj;
static C2D_Font   s_font      = NULL;

static Gui_state s_state;

bool gui_init(void) {
    memset(&s_state, 0, sizeof(s_state));
    s_state.show_overlay = true;
    s_state.show_debug   = false;
    s_state.last_fps_time = osGetTime();

    s_text_buf = C2D_TextBufNew(1024);
    if (!s_text_buf) return false;

    // Load the system font (works for Latin characters; NULL = default)
    s_font = C2D_FontLoadSystem(CFG_REGION_USA);
    // If USA fails, try JPN as fallback
    if (!s_font) s_font = C2D_FontLoadSystem(CFG_REGION_JPN);

    // Pre-warm the text buffer with a dummy parse so the first frame isn't slow
    C2D_TextFontParse(&s_text_obj, s_font, s_text_buf, " ");
    C2D_TextOptimize(&s_text_obj);
    C2D_TextBufClear(s_text_buf);

    return true;
}

void gui_deinit(void) {
    if (s_font) {
        C2D_FontFree(s_font);
        s_font = NULL;
    }
    if (s_text_buf) {
        C2D_TextBufDelete(s_text_buf);
        s_text_buf = NULL;
    }
}

void gui_update_frame(bool connected, const char *ip, int port) {
    // ---- FPS counter ----
    s_state.frame_count++;
    u64 now = osGetTime();
    if (now - s_state.last_fps_time >= 1000) {
        s_state.fps = s_state.frame_count;
        s_state.frame_count = 0;
        s_state.last_fps_time = now;
    }

    // ---- Status line ----
    if (connected) {
        snprintf(s_state.status_line, sizeof(s_state.status_line),
                 "Connected  |  %s:%d", ip ? ip : "?", port);
    } else {
        snprintf(s_state.status_line, sizeof(s_state.status_line),
                 "Waiting for stream...");
    }

    // ---- Stream info ----
    snprintf(s_state.stream_info, sizeof(s_state.stream_info),
             "256x192  |  %d/%d FPS", s_state.fps, 15);

    // ---- Debug line ----
    snprintf(s_state.debug_line, sizeof(s_state.debug_line),
             "Rendered frames: %d", s_state.frame_count);
}

void gui_draw_text(const char *text, float x, float y, float scale, u32 color) {
    if (!s_text_buf || !text || !text[0]) return;

    C2D_TextBufClear(s_text_buf);
    C2D_TextFontParse(&s_text_obj, s_font, s_text_buf, text);
    C2D_TextOptimize(&s_text_obj);
    C2D_DrawText(&s_text_obj, C2D_WithColor, x, y, 0.5f, scale, scale, color);
}

void gui_draw_rect(float x, float y, float w, float h, u32 color) {
    C2D_DrawRectSolid(x, y, 0.5f, w, h, color);
}

static void draw_top_bar(void) {
    // Semi-transparent dark background
    gui_draw_rect(0.0f, 0.0f, 400.0f, GUI_TOP_BAR_H, GUI_TRANS_BLACK);

    // Left: app name
    gui_draw_text("3DS Streaming", GUI_MARGIN, 1.0f, 0.4f, GUI_WHITE);

    // Right: stream info (resolution + FPS)
    float w = 0.0f, h = 0.0f;
    C2D_TextBufClear(s_text_buf);
    C2D_TextFontParse(&s_text_obj, s_font, s_text_buf, s_state.stream_info);
    C2D_TextOptimize(&s_text_obj);
    C2D_TextGetDimensions(&s_text_obj, 0.4f, 0.4f, &w, &h);
    C2D_DrawText(&s_text_obj, C2D_WithColor, 400.0f - w - GUI_MARGIN, 1.0f,
                 0.5f, 0.4f, 0.4f, GUI_WHITE);
}

static void draw_bottom_bar(void) {
    float y = 240.0f - GUI_BOT_BAR_H;
    gui_draw_rect(0.0f, y, 400.0f, GUI_BOT_BAR_H, GUI_TRANS_BLACK);

    // Left: status line
    gui_draw_text(s_state.status_line, GUI_MARGIN, y + 1.0f, 0.35f, GUI_LIGHT_GRAY);

    // Right: controls hint
    const char *hint = "START=Exit  SELECT=Keyframe  X=Toggle UI  Y=Debug";
    float w = 0.0f, h = 0.0f;
    C2D_TextBufClear(s_text_buf);
    C2D_TextFontParse(&s_text_obj, s_font, s_text_buf, hint);
    C2D_TextOptimize(&s_text_obj);
    C2D_TextGetDimensions(&s_text_obj, 0.35f, 0.35f, &w, &h);
    C2D_DrawText(&s_text_obj, C2D_WithColor, 400.0f - w - GUI_MARGIN, y + 1.0f,
                 0.5f, 0.35f, 0.35f, GUI_LIGHT_GRAY);
}

static void draw_debug_panel(void) {
    if (!s_state.show_debug) return;

    float panel_w = 180.0f;
    float panel_h = 105.0f;
    float x = 400.0f - panel_w - GUI_MARGIN;
    float y = GUI_TOP_BAR_H + GUI_MARGIN;

    // Background
    gui_draw_rect(x, y, panel_w, panel_h, GUI_TRANS_BLACK);

    // Border
    u32 border = GUI_GRAY;
    gui_draw_rect(x,         y,           panel_w, 1.0f, border); // top
    gui_draw_rect(x,         y + panel_h - 1.0f, panel_w, 1.0f, border); // bottom
    gui_draw_rect(x,         y,           1.0f, panel_h, border); // left
    gui_draw_rect(x + panel_w - 1.0f, y, 1.0f, panel_h, border); // right

    // Title
    gui_draw_text("Debug", x + 4.0f, y + 2.0f, 0.4f, GUI_YELLOW);

    // Content
    gui_draw_text(s_state.debug_line, x + 4.0f, y + 18.0f, 0.35f, GUI_WHITE);

    char buf[64];
    snprintf(buf, sizeof(buf), "Overlay: %s", s_state.show_overlay ? "ON" : "OFF");
    gui_draw_text(buf, x + 4.0f, y + 32.0f, 0.35f, GUI_WHITE);

    // UDP diagnostics
    snprintf(buf, sizeof(buf), "UDP pkts:%lu bytes:%lu",
             s_state.udp_packets, s_state.udp_bytes);
    gui_draw_text(buf, x + 4.0f, y + 46.0f, 0.30f, GUI_YELLOW);

    snprintf(buf, sizeof(buf), "Dropped:%lu", s_state.udp_errors);
    gui_draw_text(buf, x + 4.0f, y + 56.0f, 0.30f, GUI_RED);

    // Frame pipeline
    snprintf(buf, sizeof(buf), "Complete:%lu Dec:%lu Disp:%lu",
             s_state.nal_frames, s_state.decoded_frames, s_state.displayed_frames);
    gui_draw_text(buf, x + 4.0f, y + 66.0f, 0.30f, GUI_GREEN);

    snprintf(buf, sizeof(buf), "Y2R: %s",
             s_state.decoded_frames > 0 ? "WORKING" : "WAITING");
    gui_draw_text(buf, x + 4.0f, y + 76.0f, 0.30f,
                  s_state.decoded_frames > 0 ? GUI_GREEN : GUI_RED);
}

void gui_draw(void) {
    if (!s_text_buf) return;

    if (s_state.show_overlay) {
        draw_top_bar();
        draw_bottom_bar();
    }

    draw_debug_panel();
}

bool gui_overlay_visible(void) { return s_state.show_overlay; }
void gui_set_overlay_visible(bool visible) { s_state.show_overlay = visible; }

bool gui_debug_visible(void) { return s_state.show_debug; }
void gui_set_debug_visible(bool visible) { s_state.show_debug = visible; }

void gui_set_udp_stats(uint32_t calls, uint32_t bytes, uint32_t packets, uint32_t errors) {
    s_state.udp_calls   = calls;
    s_state.udp_bytes   = bytes;
    s_state.udp_packets = packets;
    s_state.udp_errors  = errors;
}

void gui_set_frame_stats(uint32_t nal_frames, uint32_t decoded_frames, uint32_t displayed_frames, uint32_t dropped_frames) {
    s_state.nal_frames       = nal_frames;
    s_state.decoded_frames   = decoded_frames;
    s_state.displayed_frames = displayed_frames;
    s_state.dropped_frames   = dropped_frames;
}

// Draw the last N log lines on the bottom screen (320x240).
void gui_draw_logs(void) {
    if (!s_text_buf) return;

    const auto &logs = GetLogs();
    if (logs.empty()) return;

    // Dark background
    gui_draw_rect(0.0f, 0.0f, 320.0f, 240.0f, GUI_BLACK);

    // Title bar
    gui_draw_rect(0.0f, 0.0f, 320.0f, 14.0f, GUI_DARK_GRAY);
    gui_draw_text("Logs", 4.0f, 1.0f, 0.4f, GUI_WHITE);

    // Draw from bottom up, newest at bottom
    float line_h = 11.0f;
    float y = 240.0f - 2.0f;
    float scale = 0.32f;
    int max_lines = 20;

    int start = (int)logs.size() - max_lines;
    if (start < 0) start = 0;

    for (int i = (int)logs.size() - 1; i >= start; i--) {
        y -= line_h;
        if (y < 16.0f) break;
        gui_draw_text(logs[i].c_str(), 4.0f, y, scale, GUI_LIGHT_GRAY);
    }
}

void gui_draw_big_status(const char *line1, const char *line2, u32 color) {
    if (!s_text_buf) return;

    float scale = 0.6f;
    float w1 = 0.0f, h1 = 0.0f;
    float w2 = 0.0f, h2 = 0.0f;

    // Measure line 1
    if (line1 && line1[0]) {
        C2D_TextBufClear(s_text_buf);
        C2D_TextFontParse(&s_text_obj, s_font, s_text_buf, line1);
        C2D_TextOptimize(&s_text_obj);
        C2D_TextGetDimensions(&s_text_obj, scale, scale, &w1, &h1);
    }

    // Measure line 2
    if (line2 && line2[0]) {
        C2D_TextBufClear(s_text_buf);
        C2D_TextFontParse(&s_text_obj, s_font, s_text_buf, line2);
        C2D_TextOptimize(&s_text_obj);
        C2D_TextGetDimensions(&s_text_obj, scale, scale, &w2, &h2);
    }

    float total_h = h1 + (line2 && line2[0] ? 4.0f + h2 : 0.0f);
    float y_start = (240.0f - total_h) / 2.0f;
    float x, y = y_start;

    // Draw line 1 centered
    if (line1 && line1[0]) {
        x = (400.0f - w1) / 2.0f;
        gui_draw_text(line1, x, y, scale, color);
        y += h1 + 4.0f;
    }

    // Draw line 2 centered
    if (line2 && line2[0]) {
        x = (400.0f - w2) / 2.0f;
        gui_draw_text(line2, x, y, scale, color);
    }
}
