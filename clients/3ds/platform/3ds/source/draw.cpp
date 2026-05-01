#include "draw.h"
#include <cstring>
#include <stdlib.h>

extern "C" void memcpy_asm_4b(void *dst, const void *src);
static inline void memcpy_4b(u8 *dst, u8 *src) { memcpy_asm_4b(dst, src); }

int Draw_convert_to_pos(int height, int width, int img_height, int img_width,
                        int pixel_size) {
  int pos = img_width * height;
  if (pos == 0) {
    pos = img_width;
  }

  pos -= (img_width - width) - img_width;
  return pos * pixel_size;
}

Result_with_string Draw_set_texture_data(Image_data *c2d_image, u8 *buf,
                                         int pic_width, int pic_height,
                                         int tex_size_x, int tex_size_y,
                                         GPU_TEXCOLOR color_format) {
  int x_max = 0;
  int y_max = 0;
  int increase_list_x[tex_size_x + 8];
  int increase_list_y[tex_size_y + 8];
  int count[2] = {0, 0};
  int c3d_pos = 0;
  int c3d_offset = 0;
  int pixel_size = 0;
  Result_with_string result = {0, NULL};

  if (color_format == GPU_RGB8) {
    pixel_size = 3;
  } else if (color_format == GPU_RGB565) {
    pixel_size = 2;
  } else {
    result.code = -1;
    result.string = "Invalid Arg";
    return result;
  }

  for (int i = 0; i <= tex_size_x; i += 4) {
    increase_list_x[i]     = 4  * pixel_size;
    increase_list_x[i + 1] = 12 * pixel_size;
    increase_list_x[i + 2] = 4  * pixel_size;
    increase_list_x[i + 3] = 44 * pixel_size;
  }
  for (int i = 0; i <= tex_size_y; i += 8) {
    increase_list_y[i]     = 2  * pixel_size;
    increase_list_y[i + 1] = 6  * pixel_size;
    increase_list_y[i + 2] = 2  * pixel_size;
    increase_list_y[i + 3] = 22 * pixel_size;
    increase_list_y[i + 4] = 2  * pixel_size;
    increase_list_y[i + 5] = 6  * pixel_size;
    increase_list_y[i + 6] = 2  * pixel_size;
    increase_list_y[i + 7] = (tex_size_x * 8 - 42) * pixel_size;
  }

  y_max = pic_height;
  x_max = pic_width;
  if (tex_size_y < y_max) y_max = tex_size_y;
  if (tex_size_x < x_max) x_max = tex_size_x;

  c2d_image->subtex->width  = (u16)x_max;
  c2d_image->subtex->height = (u16)y_max;
  c2d_image->subtex->left   = 0.0;
  c2d_image->subtex->top    = 1.0;
  c2d_image->subtex->right  = x_max / (float)tex_size_x;
  c2d_image->subtex->bottom = 1.0 - y_max / (float)tex_size_y;
  c2d_image->c2d.subtex     = c2d_image->subtex;

  if (pixel_size == 2) {
    for (int k = 0; k < y_max; k++) {
      for (int i = 0; i < x_max; i += 2) {
        memcpy_4b(
            &(((u8 *)c2d_image->c2d.tex->data)[c3d_pos + c3d_offset]),
            &(((u8 *)buf)[Draw_convert_to_pos(k, i, pic_height, pic_width, pixel_size)]));
        c3d_pos += increase_list_x[count[0]];
        count[0]++;
      }
      count[0] = 0;
      c3d_pos   = 0;
      c3d_offset += increase_list_y[count[1]];
      count[1]++;
    }
  }

  C3D_TexFlush(c2d_image->c2d.tex);
  return result;
}

Result_with_string Draw_c2d_image_init(Image_data *c2d_image, int tex_size_x,
                                       int tex_size_y,
                                       GPU_TEXCOLOR color_format) {
  Result_with_string result = {0, NULL};

  c2d_image->subtex  = (Tex3DS_SubTexture *)linearAlloc(sizeof(Tex3DS_SubTexture));
  c2d_image->c2d.tex = (C3D_Tex *)linearAlloc(sizeof(C3D_Tex));

  if (!c2d_image->subtex || !c2d_image->c2d.tex) {
    if (c2d_image->subtex)  linearFree(c2d_image->subtex);
    if (c2d_image->c2d.tex) linearFree(c2d_image->c2d.tex);
    c2d_image->subtex  = NULL;
    c2d_image->c2d.tex = NULL;
    result.code   = -1;
    result.string = "Out of linear memory";
    return result;
  }
  c2d_image->c2d.subtex = c2d_image->subtex;

  if (!C3D_TexInit(c2d_image->c2d.tex, (u16)tex_size_x, (u16)tex_size_y, color_format)) {
    linearFree(c2d_image->c2d.tex);
    linearFree(c2d_image->subtex);
    c2d_image->c2d.tex = NULL;
    c2d_image->subtex  = NULL;
    result.code   = -1;
    result.string = "TexInit failed";
    return result;
  }

  C3D_TexSetFilter(c2d_image->c2d.tex, GPU_LINEAR, GPU_LINEAR);
  c2d_image->c2d.tex->border = 0xFFFFFF;
  C3D_TexSetWrap(c2d_image->c2d.tex, GPU_CLAMP_TO_EDGE, GPU_CLAMP_TO_EDGE);

  return result;
}

void Draw_c2d_image_free(Image_data c2d_image) {
  if (c2d_image.c2d.tex) {
    if (c2d_image.c2d.tex->data) linearFree(c2d_image.c2d.tex->data);
    linearFree(c2d_image.c2d.tex);
  }
  if (c2d_image.subtex) linearFree(c2d_image.subtex);
}

void Draw_texture(C2D_Image image, float x, float y, float x_size, float y_size) {
  C2D_DrawParams c2d_parameter = {
      {x, y, x_size, y_size}, {0.0f, 0.0f}, 0.0f, 0.0f};
  if (image.tex != NULL) {
    C2D_DrawImage(image, &c2d_parameter, NULL);
  }
}
