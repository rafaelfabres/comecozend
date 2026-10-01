#include "bridge.h"

#include <SDL2/SDL.h>
#include <SDL2/SDL_ttf.h>
#include <string.h>

static SDL_Window *g_window = NULL;
static SDL_Renderer *g_renderer = NULL;
static TTF_Font *g_font = NULL;
static TTF_Font *g_font_small = NULL;

static const SDL_Color COLOR_TEXT = {230, 230, 230, 255};
static const SDL_Color COLOR_TEXT_DIM = {150, 150, 150, 255};
static const SDL_Color COLOR_SELECTED_BG = {50, 90, 160, 255};
static const SDL_Color COLOR_BG = {23, 23, 23, 255};      // itch.io page background
static const SDL_Color COLOR_ACCENT = {250, 92, 92, 255};  // itch.io signature red
static const SDL_Color COLOR_FOOTER_BG = {30, 30, 36, 255};

static SDL_GameController *g_controller = NULL;
static char g_font_path[1024];

static void draw_text_cached(int slot, TTF_Font *font, int x, int y, const char *text, SDL_Color color);
static const char *truncate_to_width(TTF_Font *font, const char *text, int max_width, char *out, size_t out_size);

// Decodes one UTF-8 codepoint, returning its byte length (0 on malformed).
static int utf8_next(const char *s, Uint32 *out) {
    const unsigned char *p = (const unsigned char *)s;
    if (p[0] < 0x80) { *out = p[0]; return p[0] ? 1 : 0; }
    if ((p[0] & 0xE0) == 0xC0 && (p[1] & 0xC0) == 0x80) {
        *out = ((Uint32)(p[0] & 0x1F) << 6) | (p[1] & 0x3F);
        return 2;
    }
    if ((p[0] & 0xF0) == 0xE0 && (p[1] & 0xC0) == 0x80 && (p[2] & 0xC0) == 0x80) {
        *out = ((Uint32)(p[0] & 0x0F) << 12) | ((Uint32)(p[1] & 0x3F) << 6) | (p[2] & 0x3F);
        return 3;
    }
    if ((p[0] & 0xF8) == 0xF0 && (p[1] & 0xC0) == 0x80 && (p[2] & 0xC0) == 0x80 && (p[3] & 0xC0) == 0x80) {
        *out = ((Uint32)(p[0] & 0x07) << 18) | ((Uint32)(p[1] & 0x3F) << 12) |
               ((Uint32)(p[2] & 0x3F) << 6) | (p[3] & 0x3F);
        return 4;
    }
    return 0;
}

// The bundled font has no CJK or emoji coverage, and SDL_ttf renders missing
// glyphs as tofu boxes (itch.io titles like "Yume Tenshi <CJK>" showed as
// squares). Drop anything the font cannot actually draw, collapsing the gap
// to a single space so words do not run together.
static const char *drop_unrenderable(TTF_Font *font, const char *text, char *out, size_t out_size) {
    size_t w = 0;
    int last_was_gap = 0;
    for (const char *p = text; *p && w + 5 < out_size; ) {
        Uint32 cp = 0;
        int len = utf8_next(p, &cp);
        if (len <= 0) { p++; continue; }
        if (TTF_GlyphIsProvided32(font, cp)) {
            memcpy(out + w, p, len);
            w += len;
            last_was_gap = 0;
        } else if (!last_was_gap) {
            out[w++] = ' ';
            last_was_gap = 1;
        }
        p += len;
    }
    // Trim a trailing gap so a stripped suffix does not leave dangling space.
    while (w > 0 && out[w - 1] == ' ') w--;
    out[w] = '\0';
    return out;
}

// Text-texture cache: software rendering (no RGA) makes re-rasterizing every
// glyph every frame expensive, and most on-screen text doesn't change
// between frames (only the selection highlight does). Cache by draw slot:
// each row/label gets a fixed slot, reused as long as its text is unchanged.
// Texture-cache slot map. Every drawing call site owns a disjoint range:
// two call sites sharing a slot means they destroy and re-rasterise each
// other's glyphs every frame, which both breaks rendering and burns CPU.
//   0-47    list rows (16 rows x 3: title/author/badge)
//   48-55   bridge_draw_text single labels (its `slot` arg, 0-7)
//   56      footer
//   64-79   filter screen rows
//   80-139  on-screen keyboard keys (40 keys today, room to grow)
//   140-169 small-font paragraphs (descriptions, panel titles)
//   170-179 large-font paragraphs (headings)
#define CACHE_SLOTS 200
#define ROW_SLOT_BASE(row) ((row) * 3)
#define TEXT_SLOT_BASE 48
#define FOOTER_SLOT 56
#define FILTER_SLOT_BASE 64
#define KEYBOARD_SLOT_BASE 80

typedef struct {
    char text[256];
    TTF_Font *font;
    SDL_Color color;
    SDL_Texture *texture;
    int w, h;
    int used;
} CachedText;

static int same_color(SDL_Color a, SDL_Color b) {
    return a.r == b.r && a.g == b.g && a.b == b.b && a.a == b.a;
}
static CachedText g_text_cache[CACHE_SLOTS];

static void get_cached_texture(int slot, TTF_Font *font, const char *text, SDL_Color color,
                                SDL_Texture **out_tex, int *out_w, int *out_h) {
    *out_tex = NULL;
    *out_w = 0;
    *out_h = 0;
    if (slot < 0 || slot >= CACHE_SLOTS || !text || text[0] == '\0') return;
    CachedText *entry = &g_text_cache[slot];
    // The colour is part of the key: the same string drawn highlighted and
    // unhighlighted is two different textures. Leaving it out meant a badge
    // rendered white while its row was selected stayed white afterwards,
    // because the text and slot had not changed.
    if (entry->used && entry->font == font && same_color(entry->color, color) &&
        strcmp(entry->text, text) == 0) {
        *out_tex = entry->texture;
        *out_w = entry->w;
        *out_h = entry->h;
        return;
    }
    if (entry->texture) {
        SDL_DestroyTexture(entry->texture);
        entry->texture = NULL;
    }
    SDL_Surface *surface = TTF_RenderUTF8_Blended(font, text, color);
    if (!surface) return;
    entry->texture = SDL_CreateTextureFromSurface(g_renderer, surface);
    entry->w = surface->w;
    entry->h = surface->h;
    SDL_FreeSurface(surface);
    strncpy(entry->text, text, sizeof(entry->text) - 1);
    entry->text[sizeof(entry->text) - 1] = '\0';
    entry->font = font;
    entry->color = color;
    entry->used = 1;
    *out_tex = entry->texture;
    *out_w = entry->w;
    *out_h = entry->h;
}

int bridge_init(const char *font_path, int window_w, int window_h) {
    if (SDL_Init(SDL_INIT_VIDEO | SDL_INIT_GAMECONTROLLER) != 0) {
        return 1;
    }
    if (SDL_NumJoysticks() > 0 && SDL_IsGameController(0)) {
        g_controller = SDL_GameControllerOpen(0);
    }
    if (TTF_Init() != 0) {
        return 2;
    }
    // window_w/window_h <= 0 means "use the display's own resolution", which
    // is what a handheld running fullscreen on KMS/DRM wants — hardcoding a
    // size letterboxes the UI or crops it.
    if (window_w <= 0 || window_h <= 0) {
        SDL_DisplayMode mode;
        if (SDL_GetCurrentDisplayMode(0, &mode) == 0) {
            window_w = mode.w;
            window_h = mode.h;
        } else {
            window_w = 640;
            window_h = 480;
        }
    }
    g_window = SDL_CreateWindow("Leaf Itch.io (dArkOS PoC)",
                                 SDL_WINDOWPOS_CENTERED, SDL_WINDOWPOS_CENTERED,
                                 window_w, window_h,
                                 SDL_WINDOW_SHOWN | SDL_WINDOW_FULLSCREEN_DESKTOP);
    if (!g_window) {
        return 3;
    }
    g_renderer = SDL_CreateRenderer(g_window, -1, SDL_RENDERER_SOFTWARE);
    if (!g_renderer) {
        return 4;
    }
    snprintf(g_font_path, sizeof(g_font_path), "%s", font_path);
    g_font = TTF_OpenFont(font_path, 22);
    g_font_small = TTF_OpenFont(font_path, 16);
    if (!g_font || !g_font_small) {
        return 5;
    }
    return 0;
}

void bridge_get_size(int *w, int *h) {
    SDL_GetWindowSize(g_window, w, h);
}

// --- Textures ---
// A small fixed table: the Go side's LRU cache decides what lives here, and
// ids are slot indices it hands out, so there is no dynamic bookkeeping in C.
#define MAX_TEXTURES 64  // 0-55 cover-art LRU (animations hold one per frame), 56 = QR
static SDL_Texture *g_textures[MAX_TEXTURES];

int bridge_upload_texture(int id, const unsigned char *rgba, int w, int h) {
    if (id < 0 || id >= MAX_TEXTURES || w <= 0 || h <= 0) return 1;
    if (g_textures[id]) {
        SDL_DestroyTexture(g_textures[id]);
        g_textures[id] = NULL;
    }
    SDL_Texture *tex = SDL_CreateTexture(g_renderer, SDL_PIXELFORMAT_ABGR8888,
                                          SDL_TEXTUREACCESS_STATIC, w, h);
    if (!tex) return 2;
    if (SDL_UpdateTexture(tex, NULL, rgba, w * 4) != 0) {
        SDL_DestroyTexture(tex);
        return 3;
    }
    SDL_SetTextureBlendMode(tex, SDL_BLENDMODE_BLEND);
    g_textures[id] = tex;
    return 0;
}

void bridge_destroy_texture(int id) {
    if (id < 0 || id >= MAX_TEXTURES) return;
    if (g_textures[id]) {
        SDL_DestroyTexture(g_textures[id]);
        g_textures[id] = NULL;
    }
}

void bridge_draw_texture_fit(int id, int x, int y, int w, int h, int alpha) {
    if (id < 0 || id >= MAX_TEXTURES || !g_textures[id]) return;
    int tw = 0, th = 0;
    SDL_QueryTexture(g_textures[id], NULL, NULL, &tw, &th);
    if (tw <= 0 || th <= 0) return;
    // Letterbox: scale by the smaller ratio so the whole image is visible.
    double scale = (double)w / tw;
    double scaleH = (double)h / th;
    if (scaleH < scale) scale = scaleH;
    int dw = (int)(tw * scale);
    int dh = (int)(th * scale);
    SDL_Rect dst = {x + (w - dw) / 2, y + (h - dh) / 2, dw, dh};
    SDL_SetTextureAlphaMod(g_textures[id], (Uint8)alpha);
    SDL_RenderCopy(g_renderer, g_textures[id], NULL, &dst);
}

void bridge_draw_texture_cover(int id, int x, int y, int w, int h, int alpha) {
    if (id < 0 || id >= MAX_TEXTURES || !g_textures[id]) return;
    int tw = 0, th = 0;
    SDL_QueryTexture(g_textures[id], NULL, NULL, &tw, &th);
    if (tw <= 0 || th <= 0) return;
    // Cover: scale by the LARGER ratio and crop the overflow via srcrect, so
    // the box is filled edge to edge with no letterbox bars.
    double scale = (double)w / tw;
    double scaleH = (double)h / th;
    if (scaleH > scale) scale = scaleH;
    int srcW = (int)(w / scale);
    int srcH = (int)(h / scale);
    if (srcW > tw) srcW = tw;
    if (srcH > th) srcH = th;
    SDL_Rect src = {(tw - srcW) / 2, (th - srcH) / 2, srcW, srcH};
    SDL_Rect dst = {x, y, w, h};
    SDL_SetTextureAlphaMod(g_textures[id], (Uint8)alpha);
    SDL_RenderCopy(g_renderer, g_textures[id], &src, &dst);
}

int bridge_measure_small(const char *text) {
    int w = 0, h = 0;
    TTF_SizeUTF8(g_font_small, text, &w, &h);
    return w;
}

// Tag chips, the equivalent of Leaf's DrawTagPills: a filled capsule per tag
// so the metadata reads as structured data instead of a run-on sentence.
int bridge_draw_pill(int slot, int x, int y, int h, const char *label) {
    int tw = bridge_measure_small(label);
    int pad = 10;
    int w = tw + pad * 2;
    SDL_Rect box = {x, y, w, h};
    SDL_SetRenderDrawBlendMode(g_renderer, SDL_BLENDMODE_BLEND);
    SDL_SetRenderDrawColor(g_renderer, 250, 92, 92, 45);
    SDL_RenderFillRect(g_renderer, &box);
    SDL_SetRenderDrawColor(g_renderer, 250, 92, 92, 120);
    SDL_RenderDrawRect(g_renderer, &box);
    int th = 0, tmp = 0;
    TTF_SizeUTF8(g_font_small, label, &tmp, &th);
    draw_text_cached(slot, g_font_small, x + pad, y + (h - th) / 2, label, COLOR_TEXT);
    return w;
}

void bridge_fill_rect(int x, int y, int w, int h, int r, int g, int b, int a) {
    SDL_Rect box = {x, y, w, h};
    SDL_SetRenderDrawBlendMode(g_renderer, SDL_BLENDMODE_BLEND);
    SDL_SetRenderDrawColor(g_renderer, (Uint8)r, (Uint8)g, (Uint8)b, (Uint8)a);
    SDL_RenderFillRect(g_renderer, &box);
}

void bridge_release_display(void) {
    for (int i = 0; i < MAX_TEXTURES; i++) {
        bridge_destroy_texture(i);
    }
    for (int i = 0; i < CACHE_SLOTS; i++) {
        if (g_text_cache[i].texture) {
            SDL_DestroyTexture(g_text_cache[i].texture);
            g_text_cache[i].texture = NULL;
            g_text_cache[i].used = 0;
        }
    }
    if (g_font) { TTF_CloseFont(g_font); g_font = NULL; }
    if (g_font_small) { TTF_CloseFont(g_font_small); g_font_small = NULL; }
    if (g_renderer) { SDL_DestroyRenderer(g_renderer); g_renderer = NULL; }
    if (g_window) { SDL_DestroyWindow(g_window); g_window = NULL; }
    if (g_controller) { SDL_GameControllerClose(g_controller); g_controller = NULL; }
    TTF_Quit();
    SDL_QuitSubSystem(SDL_INIT_VIDEO | SDL_INIT_GAMECONTROLLER);
    SDL_Quit();
}

int bridge_reacquire_display(const char *font_path) {
    return bridge_init(font_path, 0, 0);
}

void bridge_shutdown(void) {
    for (int i = 0; i < MAX_TEXTURES; i++) {
        bridge_destroy_texture(i);
    }
    for (int i = 0; i < CACHE_SLOTS; i++) {
        if (g_text_cache[i].texture) {
            SDL_DestroyTexture(g_text_cache[i].texture);
            g_text_cache[i].texture = NULL;
            g_text_cache[i].used = 0;
        }
    }
    if (g_controller) SDL_GameControllerClose(g_controller);
    if (g_font) TTF_CloseFont(g_font);
    if (g_font_small) TTF_CloseFont(g_font_small);
    if (g_renderer) SDL_DestroyRenderer(g_renderer);
    if (g_window) SDL_DestroyWindow(g_window);
    TTF_Quit();
    SDL_Quit();
}

// Keyboard stand-in for the gamepad, matching appui.Button semantics.
// A real dArkOS build reads /dev/input via SDL_GameController instead; the
// mapping table below is the only thing that would need to change.
// Real gamepad input, used automatically whenever the device exposes an
// SDL_GameController (this is what a real dArkOS/Miniloong build hits;
// map_key/keyboard above only exists for testing on a PC with no pad).
static void map_pad_button(Uint8 button, BridgeInput *in, int value) {
    switch (button) {
        case SDL_CONTROLLER_BUTTON_DPAD_UP:    in->up = value; break;
        case SDL_CONTROLLER_BUTTON_DPAD_DOWN:  in->down = value; break;
        case SDL_CONTROLLER_BUTTON_DPAD_LEFT:  in->left = value; break;
        case SDL_CONTROLLER_BUTTON_DPAD_RIGHT: in->right = value; break;
        case SDL_CONTROLLER_BUTTON_A:          in->b = value; break;
        case SDL_CONTROLLER_BUTTON_B:          in->a = value; break;
        case SDL_CONTROLLER_BUTTON_X:          in->x = value; break;
        case SDL_CONTROLLER_BUTTON_Y:          in->y = value; break;
        case SDL_CONTROLLER_BUTTON_LEFTSHOULDER:  in->l1 = value; break;
        case SDL_CONTROLLER_BUTTON_RIGHTSHOULDER: in->r1 = value; break;
        case SDL_CONTROLLER_BUTTON_START:  in->start = value; break;
        case SDL_CONTROLLER_BUTTON_BACK:   in->sel = value; break;
        default: break;
    }
}

// L2/R2 are analog triggers on SDL_GameController, not digital buttons, so
// they arrive as axis motion rather than button-down/up events.
#define TRIGGER_PRESS_THRESHOLD 8000

static void map_pad_axis(Uint8 axis, Sint16 value, BridgeInput *in, int *pressed) {
    int down = value > TRIGGER_PRESS_THRESHOLD;
    if (axis == SDL_CONTROLLER_AXIS_TRIGGERLEFT) {
        in->l2 = 1;
        *pressed = down;
    } else if (axis == SDL_CONTROLLER_AXIS_TRIGGERRIGHT) {
        in->r2 = 1;
        *pressed = down;
    }
}

static void map_key(SDL_Keycode key, BridgeInput *in, int value) {
    switch (key) {
        case SDLK_UP:     in->up = value; break;
        case SDLK_DOWN:   in->down = value; break;
        case SDLK_LEFT:   in->left = value; break;
        case SDLK_RIGHT:  in->right = value; break;
        case SDLK_RETURN: in->a = value; break;      // A
        case SDLK_ESCAPE: in->b = value; break;       // B
        case SDLK_x:      in->x = value; break;
        case SDLK_y:      in->y = value; break;
        case SDLK_q:      in->l1 = value; break;
        case SDLK_e:      in->r1 = value; break;
        case SDLK_1:      in->l2 = value; break;
        case SDLK_3:      in->r2 = value; break;
        case SDLK_TAB:    in->sel = value; break;
        case SDLK_SPACE:  in->start = value; break;
        default: break;
    }
}

int bridge_poll(BridgeInput *out, int *pressed) {
    memset(out, 0, sizeof(*out));
    SDL_Event event;
    if (!SDL_WaitEventTimeout(&event, 100)) {
        return 0;
    }
    if (event.type == SDL_QUIT) {
        out->quit = 1;
        *pressed = 1;
        return 1;
    }
    if (event.type == SDL_KEYDOWN && !event.key.repeat) {
        map_key(event.key.keysym.sym, out, 1);
        *pressed = 1;
        return 1;
    }
    if (event.type == SDL_KEYUP) {
        map_key(event.key.keysym.sym, out, 1);
        *pressed = 0;
        return 1;
    }
    if (event.type == SDL_CONTROLLERDEVICEADDED && !g_controller) {
        g_controller = SDL_GameControllerOpen(event.cdevice.which);
        return 0;
    }
    if (event.type == SDL_CONTROLLERDEVICEREMOVED && g_controller) {
        SDL_GameControllerClose(g_controller);
        g_controller = NULL;
        return 0;
    }
    if (event.type == SDL_CONTROLLERBUTTONDOWN) {
        map_pad_button(event.cbutton.button, out, 1);
        *pressed = 1;
        return 1;
    }
    if (event.type == SDL_CONTROLLERBUTTONUP) {
        map_pad_button(event.cbutton.button, out, 1);
        *pressed = 0;
        return 1;
    }
    if (event.type == SDL_CONTROLLERAXISMOTION) {
        map_pad_axis(event.caxis.axis, event.caxis.value, out, pressed);
        return 1;
    }
    return 0;
}

void bridge_begin_frame(void) {
    SDL_SetRenderDrawColor(g_renderer, COLOR_BG.r, COLOR_BG.g, COLOR_BG.b, 255);
    SDL_RenderClear(g_renderer);

    int w = 0, h = 0;
    SDL_GetWindowSize(g_window, &w, &h);
    // Fixed itch.io-themed chrome: an accent rule under the header and a
    // slightly lighter content panel. Costs two filled rects per frame,
    // versus a full-screen texture blit plus alpha wash for the old
    // per-game backdrop.
    SDL_Rect accent = {0, 0, w, 3};
    SDL_SetRenderDrawColor(g_renderer, COLOR_ACCENT.r, COLOR_ACCENT.g, COLOR_ACCENT.b, 255);
    SDL_RenderFillRect(g_renderer, &accent);
}

void bridge_present(void) {
    SDL_RenderPresent(g_renderer);
}

static void draw_text_cached(int slot, TTF_Font *font, int x, int y, const char *text, SDL_Color color) {
    char clean[1024];
    text = drop_unrenderable(font, text, clean, sizeof(clean));
    SDL_Texture *texture;
    int w, h;
    get_cached_texture(slot, font, text, color, &texture, &w, &h);
    if (!texture) return;
    SDL_Rect dst = {x, y, w, h};
    SDL_RenderCopy(g_renderer, texture, NULL, &dst);
}

void bridge_draw_text_bright(int slot, int x, int y, const char *text) {
    draw_text_cached(slot, g_font_small, x, y, text, COLOR_TEXT);
}

void bridge_draw_manage_row(int slot_base, int x, int y, int row_h, int w,
                            const char *label, const char *detail, const char *badge) {
    int badge_w = 0;
    if (badge && badge[0] != '\0') {
        badge_w = bridge_measure_small(badge);
        draw_text_cached(slot_base + 2, g_font_small, x + w - badge_w,
                          y + (row_h - TTF_FontHeight(g_font_small)) / 2,
                          badge, COLOR_TEXT_DIM);
    }
    int text_max = w - badge_w - 24;
    if (text_max < 60) text_max = 60;

    // Same block-centring as the catalog rows: fixed offsets left the
    // name/detail pair sitting high, so the second line read as detached.
    int label_h = TTF_FontHeight(g_font);
    int detail_h = TTF_FontHeight(g_font_small);
    int has_detail = (detail && detail[0] != '\0');
    const int gap = 1;
    int block_h = label_h + (has_detail ? gap + detail_h : 0);
    int block_y = y + (row_h - block_h) / 2;

    char label_buf[512], detail_buf[512];
    draw_text_cached(slot_base + 0, g_font, x, block_y,
                      truncate_to_width(g_font, label, text_max, label_buf, sizeof(label_buf)),
                      COLOR_TEXT);
    if (has_detail) {
        draw_text_cached(slot_base + 1, g_font_small, x, block_y + label_h + gap,
                          truncate_to_width(g_font_small, detail, text_max, detail_buf, sizeof(detail_buf)),
                          COLOR_TEXT_DIM);
    }
}

void bridge_draw_text(int slot, int x, int y, const char *text, int emphasized) {
    draw_text_cached(TEXT_SLOT_BASE + (slot % 8), emphasized ? g_font : g_font_small, x, y, text,
                      emphasized ? COLOR_TEXT : COLOR_TEXT_DIM);
}

// truncate_to_width shortens text (UTF-8, but this is byte-based which is
// fine for ASCII game titles; a truly correct version would walk runes) to
// fit within max_width pixels for the given font, appending "..." when
// something was cut. Writes into out (size out_size) and returns it.
static const char *truncate_to_width(TTF_Font *font, const char *text, int max_width,
                                      char *out, size_t out_size) {
    int w = 0, h = 0;
    TTF_SizeUTF8(font, text, &w, &h);
    if (w <= max_width) {
        snprintf(out, out_size, "%s", text);
        return out;
    }
    size_t len = strlen(text);
    while (len > 0) {
        len--;
        snprintf(out, out_size, "%.*s...", (int)len, text);
        TTF_SizeUTF8(font, out, &w, &h);
        if (w <= max_width || len == 0) {
            break;
        }
    }
    return out;
}

void bridge_draw_row(int row_index, int y, int row_h, const char *title, const char *author,
                     const char *badge, int selected) {
    int w = 0, h = 0;
    SDL_GetWindowSize(g_window, &w, &h);
    bridge_draw_row_w(row_index, y, row_h, w, title, author, badge, selected, 0);
}

void bridge_draw_row_w(int row_index, int y, int row_h, int list_w, const char *title,
                       const char *author, const char *badge, int selected, int installed) {
    int w = list_w;
    if (selected) {
        SDL_Rect bg = {0, y, w, row_h};
        SDL_SetRenderDrawColor(g_renderer, COLOR_SELECTED_BG.r, COLOR_SELECTED_BG.g,
                                COLOR_SELECTED_BG.b, 255);
        SDL_RenderFillRect(g_renderer, &bg);
        // Accent bar on the leading edge, so the selection still reads at a
        // glance on a washed-out handheld panel.
        SDL_Rect bar = {0, y, 4, row_h};
        SDL_SetRenderDrawColor(g_renderer, COLOR_ACCENT.r, COLOR_ACCENT.g, COLOR_ACCENT.b, 255);
        SDL_RenderFillRect(g_renderer, &bar);
    } else {
        // Hairline separator between rows.
        SDL_Rect rule = {16, y + row_h - 1, w - 32, 1};
        SDL_SetRenderDrawBlendMode(g_renderer, SDL_BLENDMODE_BLEND);
        SDL_SetRenderDrawColor(g_renderer, 255, 255, 255, 18);
        SDL_RenderFillRect(g_renderer, &rule);
    }

    int base = ROW_SLOT_BASE(row_index % 16);
    int left = 18;

    // Already-installed games get a green marker, so the list shows at a
    // glance what is on the SD card without opening the downloads window.
    if (installed) {
        SDL_Rect dot = {left, y + row_h / 2 - 4, 8, 8};
        SDL_SetRenderDrawColor(g_renderer, 90, 190, 110, 255);
        SDL_RenderFillRect(g_renderer, &dot);
        left += 18;
    }

    // Right-align the badge against the column edge and measure it, so the
    // title/description get every remaining pixel instead of a fixed guess.
    int badge_w = 0;
    // A badge starting with \x01 is a warning ("demo gone"): drawn in red,
    // with a red mark at the row's left edge.
    int warn = badge && badge[0] == '\x01';
    int gold = badge && badge[0] == '\x02';  // mastered on RetroAchievements
    if (warn || gold) badge++;
    if (badge && badge[0] != '\0') {
        badge_w = bridge_measure_small(badge);
        SDL_Color red = {255, 110, 110, 255};
        SDL_Color amber = {255, 200, 90, 255};
        SDL_Color color = selected ? COLOR_TEXT : COLOR_TEXT_DIM;
        if (warn) color = selected ? (SDL_Color){255, 215, 215, 255} : red;
        if (gold) color = amber;
        draw_text_cached(base + 2, g_font_small, w - badge_w - 16,
                          y + (row_h - TTF_FontHeight(g_font_small)) / 2, badge, color);
    }
    if (warn || gold) {
        SDL_Rect mark = {0, y + 6, 4, row_h - 12};
        if (gold) SDL_SetRenderDrawColor(g_renderer, 235, 185, 60, 255);
        else SDL_SetRenderDrawColor(g_renderer, 230, 60, 60, 255);
        SDL_RenderFillRect(g_renderer, &mark);
    }

    int text_max = w - left - badge_w - 40;
    if (text_max < 80) text_max = 80;

    // Centre the title+description block vertically as a unit. Fixed
    // offsets left the pair sitting high in the row, so the description
    // looked detached from the title rather than part of the same entry.
    int title_h = TTF_FontHeight(g_font);
    int author_h = TTF_FontHeight(g_font_small);
    int has_author = (author && author[0] != '\0');
    const int gap = 1;
    int block_h = title_h + (has_author ? gap + author_h : 0);
    int block_y = y + (row_h - block_h) / 2;

    char title_buf[512];
    char author_buf[512];
    draw_text_cached(base + 0, g_font, left, block_y,
                      truncate_to_width(g_font, title, text_max, title_buf, sizeof(title_buf)),
                      COLOR_TEXT);
    if (has_author) {
        draw_text_cached(base + 1, g_font_small, left, block_y + title_h + gap,
                          truncate_to_width(g_font_small, author, text_max, author_buf, sizeof(author_buf)),
                          COLOR_TEXT_DIM);
    }
}

void bridge_draw_footer(const char *hint_text) {
    int w = 0, h = 0;
    SDL_GetWindowSize(g_window, &w, &h);
    SDL_Rect bg = {0, h - 36, w, 36};
    SDL_SetRenderDrawColor(g_renderer, COLOR_FOOTER_BG.r, COLOR_FOOTER_BG.g,
                            COLOR_FOOTER_BG.b, 255);
    SDL_RenderFillRect(g_renderer, &bg);
    // A hint wider than the screen is squeezed horizontally to fit instead
    // of running off the edge (the game page has many buttons).
    char clean[1024];
    const char *text = drop_unrenderable(g_font_small, hint_text, clean, sizeof(clean));
    SDL_Texture *texture;
    int tw, th;
    get_cached_texture(FOOTER_SLOT, g_font_small, text, COLOR_TEXT_DIM, &texture, &tw, &th);
    if (!texture) return;
    int maxw = w - 48;
    SDL_Rect dst = {24, h - 28, tw, th};
    if (tw > maxw && tw > 0) {
        dst.w = maxw;
        if (maxw * 100 / tw < 70) {
            // Too much squeeze reads badly: shrink both ways a little too.
            dst.h = th * 85 / 100;
            dst.y = h - 18 - dst.h / 2;
        }
    }
    SDL_RenderCopy(g_renderer, texture, NULL, &dst);
}

// Naive greedy word-wrap. Good enough for a PoC; the real thing would cache
// wrapped layout per-paragraph instead of re-measuring every frame.
void bridge_draw_key(int slot, int x, int y, int w, int h, const char *label, int selected) {
    SDL_Rect box = {x, y, w, h};
    SDL_SetRenderDrawBlendMode(g_renderer, SDL_BLENDMODE_BLEND);
    if (selected) {
        SDL_SetRenderDrawColor(g_renderer, COLOR_SELECTED_BG.r, COLOR_SELECTED_BG.g,
                                COLOR_SELECTED_BG.b, 255);
        SDL_RenderFillRect(g_renderer, &box);
        // Accent outline, doubled so the focused key is unmistakable on a
        // low-contrast handheld panel.
        SDL_SetRenderDrawColor(g_renderer, COLOR_ACCENT.r, COLOR_ACCENT.g, COLOR_ACCENT.b, 255);
        SDL_RenderDrawRect(g_renderer, &box);
        SDL_Rect inner = {x + 1, y + 1, w - 2, h - 2};
        SDL_RenderDrawRect(g_renderer, &inner);
    } else {
        SDL_SetRenderDrawColor(g_renderer, 48, 48, 56, 255);
        SDL_RenderFillRect(g_renderer, &box);
        SDL_SetRenderDrawColor(g_renderer, 78, 78, 88, 255);
        SDL_RenderDrawRect(g_renderer, &box);
    }

    int tw = 0, th = 0;
    TTF_SizeUTF8(g_font_small, label, &tw, &th);
    int tx = x + (w - tw) / 2;
    int ty = y + (h - th) / 2;
    draw_text_cached(slot, g_font_small, tx, ty, label, COLOR_TEXT);
}
static void draw_paragraph_font(int slot_base, int slot_max, TTF_Font *font, int x, int y,
                                int max_width, const char *text, int line_h);

// Each paragraph call site needs its own slot range. Sharing one range made
// the detail screen's title and description destroy and re-rasterise each
// other's glyph textures on every single frame — both a rendering bug (text
// vanishing) and the main source of on-device slowdown.
#define PARA_SMALL_BASE 140
#define PARA_SMALL_MAX  169
#define PARA_LARGE_BASE 170
#define PARA_LARGE_MAX  179

// Slot range and draw switch for the paragraph renderer: the game page's
// status card uses its own range (so it does not fight the description for
// cached glyph textures), and line counting draws nothing.
#define PARA_CARD_BASE 180
#define PARA_CARD_MAX  195
static int g_para_base = PARA_SMALL_BASE, g_para_max = PARA_SMALL_MAX, g_para_draw = 1;

int bridge_draw_card_text(int part, int x, int y, int max_width, int max_height, const char *text) {
    // part 0 and 1 get separate halves of the range (status card, message).
    g_para_base = PARA_CARD_BASE + (part ? 8 : 0);
    g_para_max = g_para_base + 7;
    int n = bridge_draw_paragraph_scroll(x, y, max_width, max_height, text, 0);
    g_para_base = PARA_SMALL_BASE; g_para_max = PARA_SMALL_MAX;
    return n;
}

int bridge_paragraph_lines(int max_width, const char *text) {
    g_para_draw = 0;
    int n = bridge_draw_paragraph_scroll(0, 0, max_width, 22 * 1000, text, 0);
    g_para_draw = 1;
    return n;
}

int bridge_draw_paragraph_scroll(int x, int y, int max_width, int max_height,
                                 const char *text, int scroll_lines) {
    // "\n" in the text starts a new line: it becomes its own " \n "
    // token so the word splitter sees it.
    char buffer[8192];
    size_t bi = 0;
    for (const char *t = text; *t && bi < sizeof(buffer) - 4; t++) {
        if (*t == '\n') {
            buffer[bi++] = ' '; buffer[bi++] = '\n'; buffer[bi++] = ' ';
        } else {
            buffer[bi++] = *t;
        }
    }
    buffer[bi] = '\0';

    const int line_h = 22;
    int max_lines = max_height / line_h;
    if (max_lines < 1) max_lines = 1;
    if (scroll_lines < 0) scroll_lines = 0;

    char line[1024] = {0};
    char *word = strtok(buffer, " ");
    int line_index = 0;   // index among all wrapped lines
    int drawn = 0;        // how many we actually painted
    int slot = g_para_base;

    // emit() paints one finished line if it falls inside the visible window.
    #define EMIT_LINE()                                                        \
        do {                                                                   \
            if (line_index >= scroll_lines && drawn < max_lines &&             \
                slot <= g_para_max) {                                          \
                if (g_para_draw)                                               \
                    draw_text_cached(slot, g_font_small, x,                    \
                                     y + drawn * line_h, line, COLOR_TEXT);    \
                slot++;                                                        \
                drawn++;                                                       \
            }                                                                  \
            line_index++;                                                      \
        } while (0)

    while (word) {
        if (strcmp(word, "\n") == 0) {
            if (line[0] != '\0') {
                EMIT_LINE();
                line[0] = '\0';
            }
            word = strtok(NULL, " ");
            continue;
        }
        char candidate[1024];
        if (line[0] == '\0') {
            snprintf(candidate, sizeof(candidate), "%s", word);
        } else {
            snprintf(candidate, sizeof(candidate), "%s %s", line, word);
        }
        int tw = 0, th = 0;
        TTF_SizeUTF8(g_font_small, candidate, &tw, &th);
        if (tw > max_width && line[0] != '\0') {
            EMIT_LINE();
            snprintf(line, sizeof(line), "%s", word);
        } else {
            snprintf(line, sizeof(line), "%s", candidate);
        }
        word = strtok(NULL, " ");
    }
    if (line[0] != '\0') {
        EMIT_LINE();
    }
    #undef EMIT_LINE
    return line_index;
}

void bridge_draw_paragraph(int x, int y, int max_width, const char *text) {
    draw_paragraph_font(PARA_SMALL_BASE, PARA_SMALL_MAX, g_font_small, x, y, max_width, text, 22);
}

void bridge_draw_paragraph_large(int x, int y, int max_width, const char *text) {
    draw_paragraph_font(PARA_LARGE_BASE, PARA_LARGE_MAX, g_font, x, y, max_width, text, 30);
}

int bridge_draw_paragraph_large_n(int x, int y, int max_width, int max_height, const char *text) {
    char buffer[2048];
    strncpy(buffer, text, sizeof(buffer) - 1);
    buffer[sizeof(buffer) - 1] = '\0';

    const int line_h = 30;
    int max_lines = max_height / line_h;
    if (max_lines < 1) max_lines = 1;

    char line[1024] = {0};
    char *word = strtok(buffer, " ");
    int drawn = 0;
    int slot = PARA_LARGE_BASE;

    while (word && drawn < max_lines) {
        char candidate[1024];
        if (line[0] == '\0') {
            snprintf(candidate, sizeof(candidate), "%s", word);
        } else {
            snprintf(candidate, sizeof(candidate), "%s %s", line, word);
        }
        int tw = 0, th = 0;
        TTF_SizeUTF8(g_font, candidate, &tw, &th);
        if (tw > max_width && line[0] != '\0') {
            if (slot <= PARA_LARGE_MAX) {
                draw_text_cached(slot++, g_font, x, y + drawn * line_h, line, COLOR_TEXT);
            }
            drawn++;
            snprintf(line, sizeof(line), "%s", word);
        } else {
            snprintf(line, sizeof(line), "%s", candidate);
        }
        word = strtok(NULL, " ");
    }
    if (line[0] != '\0' && drawn < max_lines && slot <= PARA_LARGE_MAX) {
        draw_text_cached(slot, g_font, x, y + drawn * line_h, line, COLOR_TEXT);
        drawn++;
    }
    return drawn;
}

static void draw_paragraph_font(int slot_base, int slot_max, TTF_Font *font, int x, int y,
                                int max_width, const char *text, int line_h) {
    char buffer[4096];
    strncpy(buffer, text, sizeof(buffer) - 1);
    buffer[sizeof(buffer) - 1] = '\0';

    char line[1024] = {0};
    char *word = strtok(buffer, " ");
    int line_y = y;
    int line_slot = slot_base;

    while (word) {
        char candidate[1024];
        if (line[0] == '\0') {
            snprintf(candidate, sizeof(candidate), "%s", word);
        } else {
            snprintf(candidate, sizeof(candidate), "%s %s", line, word);
        }
        int tw = 0, th = 0;
        TTF_SizeUTF8(font, candidate, &tw, &th);
        if (tw > max_width && line[0] != '\0') {
            if (line_slot <= slot_max) {
                draw_text_cached(line_slot++, font, x, line_y, line, COLOR_TEXT);
            }
            line_y += line_h;
            snprintf(line, sizeof(line), "%s", word);
        } else {
            snprintf(line, sizeof(line), "%s", candidate);
        }
        word = strtok(NULL, " ");
    }
    if (line[0] != '\0' && line_slot <= slot_max) {
        draw_text_cached(line_slot, font, x, line_y, line, COLOR_TEXT);
    }
}

// Footer drawn as button "chips": the button in a small box, then what it
// does ("[A] Download   [Y] Hide ..."). spec holds one item per line, the
// button and its label separated by a tab. If everything does not fit, the
// whole row is scaled down evenly (never cut off at the edge).
static void chip_text(const char *text, SDL_Color color, int x, int y, float scale, int *out_w) {
    char clean[256];
    text = drop_unrenderable(g_font_small, text, clean, sizeof(clean));
    SDL_Surface *surf = TTF_RenderUTF8_Blended(g_font_small, text, color);
    if (!surf) { *out_w = 0; return; }
    SDL_Texture *tex = SDL_CreateTextureFromSurface(g_renderer, surf);
    SDL_Rect dst = {x, y, (int)(surf->w * scale), (int)(surf->h * scale)};
    *out_w = dst.w;
    if (tex) {
        SDL_RenderCopy(g_renderer, tex, NULL, &dst);
        SDL_DestroyTexture(tex);
    }
    SDL_FreeSurface(surf);
}

void bridge_draw_footer_chips(const char *spec) {
    int w = 0, h = 0;
    SDL_GetWindowSize(g_window, &w, &h);
    SDL_Rect bg = {0, h - 36, w, 36};
    SDL_SetRenderDrawColor(g_renderer, COLOR_FOOTER_BG.r, COLOR_FOOTER_BG.g, COLOR_FOOTER_BG.b, 255);
    SDL_RenderFillRect(g_renderer, &bg);
    SDL_SetRenderDrawColor(g_renderer, 48, 48, 58, 255);
    SDL_RenderDrawLine(g_renderer, 0, h - 36, w, h - 36);

    enum { MAXI = 12 };
    char keys[MAXI][32], labels[MAXI][64];
    int n = 0;
    const char *p = spec;
    while (*p && n < MAXI) {
        const char *end = strchr(p, '\n');
        size_t len = end ? (size_t)(end - p) : strlen(p);
        const char *tab = memchr(p, '\t', len);
        size_t full = tab ? (size_t)(tab - p) : len;
        size_t klen = full > 31 ? 31 : full;
        memcpy(keys[n], p, klen); keys[n][klen] = 0;
        labels[n][0] = 0;
        if (tab) {
            // Measured from the real tab, not the shortened key: with a
            // key past 31 bytes the label used to start inside the key and
            // the copy read past the end of the item.
            size_t llen = len - full - 1;
            if (llen > 63) llen = 63;
            memcpy(labels[n], tab + 1, llen); labels[n][llen] = 0;
        }
        n++;
        if (!end) break;
        p = end + 1;
    }

    // Never scale text (scaled glyphs look blurry and uneven on this
    // screen): tighten the spacing first, then leave out items from the
    // end, keeping the last one (Back/Exit).
    int padX = 6, keyGap = 6, itemGap = 18;
    int keep[MAXI];
    for (int i = 0; i < n; i++) keep[i] = 1;
    int avail = w - 32, tw, th;
    for (int pass = 0; pass < 2 + MAXI; pass++) {
        int total = 0, shown = 0;
        for (int i = 0; i < n; i++) {
            if (!keep[i]) continue;
            if (shown++) total += itemGap;
            if (keys[i][0]) {
                TTF_SizeUTF8(g_font_small, keys[i], &tw, &th);
                total += tw + 2 * padX + keyGap;
            }
            TTF_SizeUTF8(g_font_small, labels[i], &tw, &th);
            total += tw;
        }
        if (total <= avail) break;
        if (pass == 0) { padX = 4; keyGap = 4; itemGap = 12; continue; }
        int dropped = 0;
        for (int i = n - 2; i >= 0 && !dropped; i--) {
            if (keep[i]) { keep[i] = 0; dropped = 1; }
        }
        if (!dropped) break;
    }
    float scale = 1.0f;
    int lineH = TTF_FontHeight(g_font_small);
    int boxH = (int)((lineH + 4) * scale);
    int y = h - 18 - boxH / 2;
    int x = 16;
    SDL_SetRenderDrawBlendMode(g_renderer, SDL_BLENDMODE_BLEND);
    for (int i = 0; i < n; i++) {
        int drawn;
        if (!keep[i]) continue;
        if (!keys[i][0]) {  // plain text item, no button
            chip_text(labels[i], COLOR_TEXT_DIM, x, y + (int)(2 * scale), scale, &drawn);
            x += drawn + (int)(itemGap * scale);
            continue;
        }
        TTF_SizeUTF8(g_font_small, keys[i], &tw, &th);
        SDL_Rect box = {x, y, (int)((tw + 2 * padX) * scale), boxH};
        SDL_SetRenderDrawColor(g_renderer, 72, 72, 86, 255);
        SDL_RenderFillRect(g_renderer, &box);
        SDL_SetRenderDrawColor(g_renderer, COLOR_ACCENT.r, COLOR_ACCENT.g, COLOR_ACCENT.b, 255);
        SDL_Rect under = {box.x, box.y + box.h - 2, box.w, 2};
        SDL_RenderFillRect(g_renderer, &under);
        chip_text(keys[i], COLOR_TEXT, x + (int)(padX * scale), y + (int)(2 * scale), scale, &drawn);
        x += box.w + (int)(keyGap * scale);
        chip_text(labels[i], COLOR_TEXT_DIM, x, y + (int)(2 * scale), scale, &drawn);
        x += drawn + (int)(itemGap * scale);
    }
}

// Backdrop cache for modal screens (Filter, Downloads, Exit). The renderer
// is software-only on this device, so redrawing the whole list (rows,
// preview, cover scaling) under a modal on every key press made those
// screens sluggish. The list is drawn once into a texture, dimmed, and
// that texture is blitted instead.
static SDL_Texture *g_backdrop = NULL;
static int g_bd_w = 0, g_bd_h = 0;

int bridge_backdrop_begin(void) {
    int w = 0, h = 0;
    SDL_GetWindowSize(g_window, &w, &h);
    if (!g_backdrop || g_bd_w != w || g_bd_h != h) {
        if (g_backdrop) SDL_DestroyTexture(g_backdrop);
        g_backdrop = SDL_CreateTexture(g_renderer, SDL_PIXELFORMAT_ARGB8888, SDL_TEXTUREACCESS_TARGET, w, h);
        g_bd_w = w; g_bd_h = h;
    }
    if (!g_backdrop) return 0;
    if (SDL_SetRenderTarget(g_renderer, g_backdrop) != 0) return 0;
    return 1;
}

void bridge_backdrop_end(void) {
    SDL_SetRenderTarget(g_renderer, NULL);
}

void bridge_draw_backdrop(void) {
    if (g_backdrop) SDL_RenderCopy(g_renderer, g_backdrop, NULL, NULL);
}
