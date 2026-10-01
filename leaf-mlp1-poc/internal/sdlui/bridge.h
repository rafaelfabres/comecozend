#ifndef LEAF_SDLUI_BRIDGE_H
#define LEAF_SDLUI_BRIDGE_H

// bridge.h/.c is the direct replacement for Leaf's catui + cat_bridge.c.
// Where cat_bridge.c called into the closed-source Catastrophe library to
// draw, this bridge calls SDL2_ttf and SDL2 primitives directly. The window
// creation / event pump plumbing below is intentionally close to what
// cat_bridge.c already does, since that part never needed Catastrophe.

typedef struct {
    int up, down, left, right;
    int a, b, x, y;
    int l1, l2, r1, r2;
    int start, sel;
    int quit;
} BridgeInput;

// Returns 0 on success, non-zero on failure (SDL_GetError() has the reason).
int bridge_init(const char *font_path, int window_w, int window_h);
void bridge_shutdown(void);

// Blocks until the next relevant key transition or a small timeout elapses.
// Fills *out and returns 1 if a button changed state, 0 on timeout/no-op.
int bridge_poll(BridgeInput *out, int *pressed);

void bridge_begin_frame(void);
void bridge_present(void);

// Row drawing for the main list screen. selected != 0 highlights the row.
// y is the top pixel of the row; row_h is the row height in pixels.
// Row drawing for the main list screen. selected != 0 highlights the row.
// y is the top pixel of the row; row_h is the row height in pixels.
// row_index identifies this row's texture-cache slots (0..15) so unchanged
// text is not re-rasterized every frame.
void bridge_draw_row(int row_index, int y, int row_h, const char *title,
                     const char *author, const char *badge, int selected);
// slot selects which cache entry this call owns (0..7); use a distinct slot
// per on-screen text element drawn via this function within one screen.
// Same as bridge_draw_row but constrained to list_w pixels of width, so the
// row never runs under the preview panel on its right.
void bridge_draw_row_w(int row_index, int y, int row_h, int list_w, const char *title,
                       const char *author, const char *badge, int selected, int installed);
// A manage-window row: label + detail on the left, badge right-aligned
// inside the given width, positioned at an arbitrary x (the list rows are
// screen-anchored; these sit inside a modal).
void bridge_draw_manage_row(int slot_base, int x, int y, int row_h, int w,
                            const char *label, const char *detail, const char *badge);
void bridge_draw_text(int slot, int x, int y, const char *text, int emphasized);
// Small font in the full-strength text colour, for labels drawn on a filled
// chip where the dimmed colour would read as disabled.
void bridge_draw_text_bright(int slot, int x, int y, const char *text);
void bridge_draw_footer(const char *hint_text);
// Footer as button chips; spec = "A\tDownload\nB\tBack".
void bridge_draw_footer_chips(const char *spec);
int bridge_backdrop_begin(void);
int bridge_draw_card_text(int part, int x, int y, int max_width, int max_height, const char *text);
int bridge_paragraph_lines(int max_width, const char *text);
void bridge_backdrop_end(void);
void bridge_draw_backdrop(void);
void bridge_draw_paragraph(int x, int y, int max_width, const char *text);
// Same, but in the large font — used for titles that may wrap.
void bridge_draw_paragraph_large(int x, int y, int max_width, const char *text);
// Large-font paragraph that reports how many lines it drew, so the caller can
// advance past a title that wrapped instead of overlapping what follows.
int bridge_draw_paragraph_large_n(int x, int y, int max_width, int max_height, const char *text);
// Scrollable paragraph: draws at most max_height pixels of text, starting
// scroll_lines lines in, and returns the TOTAL number of wrapped lines so the
// caller can compute scroll bounds (Leaf does the same via SetScrollBounds).
int bridge_draw_paragraph_scroll(int x, int y, int max_width, int max_height,
                                 const char *text, int scroll_lines);
// Draws a small selectable box (an on-screen-keyboard key). slot is this
// key's own cache slot for its label texture.
void bridge_draw_key(int slot, int x, int y, int w, int h, const char *label, int selected);
// Reports the actual window size (may differ from what was requested when
// running fullscreen at the display's native resolution).
void bridge_get_size(int *w, int *h);

// Releases the display entirely (window, renderer, textures, SDL video) so
// another process can take over KMS/DRM, and restores it afterwards. Needed
// to hand the screen to RetroArch: two processes cannot hold the display at
// once, which is what "Could not queue pageflip" means.
void bridge_release_display(void);
int bridge_reacquire_display(const char *font_path);

// --- Textures (cover art) ---
// Uploads raw RGBA8888 pixels as a texture under `id`, replacing any
// previous texture with that id. Must be called from the GUI thread.
// Returns 0 on success.
int bridge_upload_texture(int id, const unsigned char *rgba, int w, int h);
void bridge_destroy_texture(int id);
// Draws texture `id` scaled to fit inside the box, preserving aspect ratio
// (letterboxed). alpha is 0-255.
void bridge_draw_texture_fit(int id, int x, int y, int w, int h, int alpha);
// Draws texture `id` scaled to COVER the box, preserving aspect ratio and
// cropping the overflow — used for the dimmed full-screen backdrop.
void bridge_draw_texture_cover(int id, int x, int y, int w, int h, int alpha);
// Fills a box with a flat colour (used for panels and dimming overlays).
void bridge_fill_rect(int x, int y, int w, int h, int r, int g, int b, int a);
// Draws a rounded-ish tag chip with its label centred; returns its width so
// the caller can flow chips across a row.
int bridge_draw_pill(int slot, int x, int y, int h, const char *label);
// Measures a label in the small font, for laying out chips before drawing.
int bridge_measure_small(const char *text);

#endif
