#pragma once

#include <3ds.h>
#include <citro2d.h>
#include <citro3d.h>
#include <stdbool.h>

#define DEF_DRAW_NO_COLOR 0xFF000000

typedef struct {
  C2D_Image c2d;
  Tex3DS_SubTexture *subtex;
} Image_data;

typedef struct {
  u32 code;
  const char *string;
} Result_with_string;

Result_with_string Draw_c2d_image_init(Image_data *c2d_image, int tex_size_x,
                                       int tex_size_y,
                                       GPU_TEXCOLOR color_format);

void Draw_c2d_image_free(Image_data c2d_image);

Result_with_string Draw_set_texture_data(Image_data *c2d_image, u8 *buf,
                                         int pic_width, int pic_height,
                                         int tex_size_x, int tex_size_y,
                                         GPU_TEXCOLOR color_format);

void Draw_texture(C2D_Image image, float x, float y, float x_size,
                  float y_size);
