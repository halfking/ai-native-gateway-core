#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
llm-gateway-go 品牌图标生成器 —— 开轩 logo 的项目化变体。

## 为什么是"变体"而不是直接复用 AI Work Mess 那套
两个项目同属开轩品牌，**造型必须一致**（同一枚开轩 logo），否则会出现
"网关和移动端不是一家"的割裂感。但每个项目的气质不同，所以只改色、不改形：

  AI Work Mess : 深靛蓝 → 兰紫 → 破晓金（金为主，金＝价值与光）
  llm-gateway  : 深靛蓝 → 青蓝 → 冰白青（青为主，青＝链路与传输）

## 与 AI Work Mess 生成器的两处关键差异（都是本项目的硬约束）
1. **保留文件名**：`/logo-icon.png`、`/logo-icon-dark.png`、`/favicon.png`
   被 `middleware/auth_mw.go:79` 的未鉴权白名单**逐字硬编码**，
   `web/src/theme.ts:93` 的 logoSrc() 也按名字取。改文件名 = 破图 + 白名单失配。
   ⇒ 本脚本只替换内容，文件名/数量/尺寸全部不变。
2. **暗色变体独立配色**：`logo-icon-dark.png` 用于暗色主题头部，
   底衬改深色系而非浅底，否则暗色页面上是一块白斑。

配色按墨迹亮度**分位秩**映射（源 logo 亮度极度集中：p25–p75 落在 20–52，
线性拉伸会把 ~87% 墨迹压成同一暗色），并把 alpha 归一化到不透明
（源 logo alpha 为二值 192，直接叠加会让底色透进来把高光洗淡）。

用法：python3 scripts/gen-brand-icons.py
"""
import os

import numpy as np
from PIL import Image, ImageDraw

ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), ".."))
BRAND = os.path.join(ROOT, "brand")

# ── 亮色变体：青蓝链路色 ────────────────────────────────────────────────
LIGHT_BG_A = np.array([0x1E, 0x3A, 0x5F], dtype=float)  # 深海军蓝（暗色主题页头衬底）
LIGHT_BG_B = np.array([0x14, 0x2A, 0x4A], dtype=float)
LIGHT_RANK_STOPS = [
    (0.00, (18, 38, 78)),    # 深水
    (0.30, (37, 99, 178)),   # 链路蓝
    (0.64, (86, 190, 233)),  # 传输青
    (1.00, (198, 240, 255)), # 冰白高光：数据通路的"抵达"
]

# ── 暗色变体：亮底反相，用于暗色主题 ──────────────────────────────────
DARK_BG_A = np.array([0xF2, 0xF7, 0xFC], dtype=float)   # 亮底
DARK_BG_B = np.array([0xDF, 0xEA, 0xF5], dtype=float)
DARK_RANK_STOPS = [
    (0.00, (10, 26, 56)),
    (0.30, (30, 84, 156)),
    (0.64, (96, 186, 226)),
    (1.00, (232, 250, 255)),
]

GLYPH_RATIO = 0.66
SS = 4  # 小尺寸超采样，避免细笔画糊


def _gradient(w: int, h: int, a: np.ndarray, b: np.ndarray) -> Image.Image:
    t = (np.linspace(0, 1, w)[None, :] + np.linspace(0, 1, h)[:, None]) / 2.0
    return Image.fromarray(((1 - t[..., None]) * a + t[..., None] * b).astype(np.uint8), "RGB")


def _rank_lut(stops) -> np.ndarray:
    lut = np.zeros((256, 3), dtype=float)
    for i in range(256):
        t = i / 255.0
        for k in range(len(stops) - 1):
            t0, c0 = stops[k]
            t1, c1 = stops[k + 1]
            if t <= t1:
                f = 0.0 if t1 == t0 else (t - t0) / (t1 - t0)
                lut[i] = np.array(c0) + max(0.0, min(1.0, f)) * (np.array(c1) - np.array(c0))
                break
        else:
            lut[i] = stops[-1][1]
    return lut.clip(0, 255).astype(np.uint8)


def recolored_logo(out_size: int, stops, lut: np.ndarray) -> Image.Image:
    """按墨迹亮度分位秩上色，裁到墨迹 bbox 后等比缩放。"""
    src = Image.open(os.path.join(BRAND, "开轩logo-亮色.png")).convert("RGBA")
    a = np.array(src).astype(float)
    alpha = a[..., 3]
    ink = alpha >= 128
    if not ink.any():
        raise SystemExit("logo 无墨迹像素")
    lum = 0.299 * a[..., 0] + 0.587 * a[..., 1] + 0.114 * a[..., 2]
    ink_lum = np.sort(lum[ink])
    rank = np.clip(np.searchsorted(ink_lum, lum, side="right") / float(len(ink_lum)), 0, 1)
    col = lut[(rank * 255).astype(int)]
    out_alpha = np.where(alpha > 0, np.clip(alpha / alpha.max() * 255.0, 0, 255), 0.0)
    img = Image.fromarray(np.dstack([col, out_alpha]).astype(np.uint8), "RGBA")
    ys, xs = np.where(ink)
    pad = 4
    img = img.crop((max(0, xs.min() - pad), max(0, ys.min() - pad),
                    min(img.width, xs.max() + pad + 1), min(img.height, ys.max() + pad + 1)))
    img.thumbnail((out_size, out_size), Image.LANCZOS)
    return img


def render_icon(size: int, stops, bg_a, bg_b, *, round_mask: bool = False,
                ss: int = SS, transparent: bool = False) -> Image.Image:
    """透明底的开轩 logo（web 头部 logo 用，保留页面底色）。"""
    lut = _rank_lut(stops)
    glyph = recolored_logo(int(size * GLYPH_RATIO), stops, lut)
    T = size * ss
    canvas = Image.new("RGBA", (T, T), (0, 0, 0, 0))
    gx, gy = (T - glyph.width) // 2, int((T - glyph.height) / 2 - T * 0.02)
    canvas.paste(glyph, (gx, gy), glyph)
    out = canvas.resize((size, size), Image.LANCZOS)
    if transparent or not round_mask:
        return out
    m = Image.new("L", (size, size), 0)
    ImageDraw.Draw(m).ellipse([0, 0, size - 1, size - 1], fill=255)
    masked = Image.new("RGBA", (size, size), (0, 0, 0, 0))
    masked.paste(out, (0, 0), m)
    return masked


def render_tile(size: int, stops, bg_a, bg_b, *, square: bool = False, ss: int = SS) -> Image.Image:
    """带底衬的方/圆图标（favicon 用）。"""
    lut = _rank_lut(stops)
    glyph = recolored_logo(int(size * GLYPH_RATIO), stops, lut)
    T = size * ss
    bg = _gradient(T, T, bg_a, bg_b).convert("RGBA")
    canvas = Image.new("RGBA", (T, T), (0, 0, 0, 0))
    gx, gy = (T - glyph.width) // 2, int((T - glyph.height) / 2 - T * 0.02)
    canvas.paste(glyph, (gx, gy), glyph)
    comp = Image.alpha_composite(bg, canvas).resize((size, size), Image.LANCZOS)
    if square:
        return comp
    m = Image.new("L", (size, size), 0)
    ImageDraw.Draw(m).rounded_rectangle([0, 0, size - 1, size - 1],
                                        radius=int(size * 0.22), fill=255)
    out = Image.new("RGBA", (size, size), (0, 0, 0, 0))
    out.paste(comp, (0, 0), m)
    return out


def save(img: Image.Image, path: str):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    img.save(path, "PNG")
    print(f"  wrote {os.path.relpath(path, ROOT)} {img.size}")


def main():
    if not os.path.isdir(BRAND):
        raise SystemExit(f"缺少 brand/ 源目录: {BRAND}")

    # 头部 logo：透明底，沿用现有 1024² 尺寸与文件名（被 auth 白名单硬编码）
    save(render_icon(1024, LIGHT_RANK_STOPS, LIGHT_BG_A, LIGHT_BG_B),
         os.path.join(ROOT, "web", "public", "logo-icon.png"))
    save(render_icon(1024, DARK_RANK_STOPS, DARK_BG_A, DARK_BG_B),
         os.path.join(ROOT, "web", "public", "logo-icon-dark.png"))

    # favicon：512² 全幅不透明方块。现有资源是 RGB 无 alpha，
    # 浏览器会按页面底色给圆角留白；改成带 alpha 的圆角砖反而会让
    # 深色标签页出现半透明边。保持"全幅不透明"以免行为漂移。
    save(render_tile(512, LIGHT_RANK_STOPS, LIGHT_BG_A, LIGHT_BG_B, square=True),
         os.path.join(ROOT, "web", "public", "favicon.png"))
    save(render_tile(512, LIGHT_RANK_STOPS, LIGHT_BG_A, LIGHT_BG_B, square=True, ss=2),
         os.path.join(ROOT, "web-mobile", "public", "favicon.png"))
    print("[gen-brand-icons] llm-gateway-go done")


if __name__ == "__main__":
    main()
