#!/usr/bin/env python3
"""Generate a client-facing VYRA demo deck in PPTX format."""

from __future__ import annotations

import argparse
import tempfile
from pathlib import Path
from typing import Iterable

from PIL import Image, ImageEnhance, ImageOps
from pptx import Presentation
from pptx.dml.color import RGBColor
from pptx.enum.shapes import MSO_SHAPE
from pptx.enum.text import MSO_ANCHOR, PP_ALIGN
from pptx.util import Inches, Pt


ROOT = Path(__file__).resolve().parent.parent
PUBLIC = ROOT / "application" / "app" / "public" / "marketing"
PRODUCT = PUBLIC / "product"
SHOTS = PUBLIC / "shots"
DEFAULT_OUTPUT = ROOT / "deliverables" / "VYRA_Client_Demo_Pitch_Deck.pptx"

SLIDE_W = Inches(13.333)
SLIDE_H = Inches(7.5)

BG = RGBColor(0x05, 0x08, 0x0F)
BG_SOFT = RGBColor(0x0C, 0x13, 0x20)
PANEL = RGBColor(0x0F, 0x18, 0x2A)
PANEL_ALT = RGBColor(0x11, 0x1F, 0x34)
LINE = RGBColor(0x3A, 0x52, 0x73)
TEXT = RGBColor(0xF3, 0xF7, 0xFF)
TEXT_MUTED = RGBColor(0xD7, 0xE2, 0xF5)
TEXT_SOFT = RGBColor(0x95, 0xAB, 0xC9)
BLUE = RGBColor(0x3E, 0xA6, 0xFF)
CYAN = RGBColor(0x5D, 0xF5, 0xFF)
MINT = RGBColor(0x49, 0xE3, 0xA1)
AMBER = RGBColor(0xFF, 0xD1, 0x66)
RED = RGBColor(0xFF, 0x6F, 0x8F)


def rgb(hex_value: str) -> RGBColor:
    hex_value = hex_value.replace("#", "")
    return RGBColor(int(hex_value[0:2], 16), int(hex_value[2:4], 16), int(hex_value[4:6], 16))


def emu_to_inches(value: int) -> float:
    return value / 914400.0


def prepare_cover_image(source: Path, dest: Path, width_px: int, height_px: int, brightness: float = 1.0) -> Path:
    with Image.open(source) as image:
        image = image.convert("RGB")
        fitted = ImageOps.fit(
            image,
            (max(width_px, 1), max(height_px, 1)),
            method=Image.Resampling.LANCZOS,
            centering=(0.5, 0.5),
        )
        if brightness != 1.0:
            fitted = ImageEnhance.Brightness(fitted).enhance(brightness)
        fitted.save(dest, format="PNG")
    return dest


def add_background(slide, accent: RGBColor) -> None:
    rect = slide.shapes.add_shape(MSO_SHAPE.RECTANGLE, 0, 0, SLIDE_W, SLIDE_H)
    rect.fill.solid()
    rect.fill.fore_color.rgb = BG
    rect.line.fill.background()

    top_band = slide.shapes.add_shape(MSO_SHAPE.RECTANGLE, Inches(0), Inches(0), SLIDE_W, Inches(0.12))
    top_band.fill.solid()
    top_band.fill.fore_color.rgb = accent
    top_band.fill.transparency = 18
    top_band.line.fill.background()

    orb_right = slide.shapes.add_shape(MSO_SHAPE.OVAL, Inches(9.2), Inches(-1.4), Inches(4.8), Inches(4.8))
    orb_right.fill.solid()
    orb_right.fill.fore_color.rgb = accent
    orb_right.fill.transparency = 86
    orb_right.line.fill.background()

    orb_left = slide.shapes.add_shape(MSO_SHAPE.OVAL, Inches(-1.1), Inches(5.2), Inches(3.2), Inches(3.2))
    orb_left.fill.solid()
    orb_left.fill.fore_color.rgb = CYAN
    orb_left.fill.transparency = 92
    orb_left.line.fill.background()

    line = slide.shapes.add_shape(MSO_SHAPE.RECTANGLE, Inches(0.75), Inches(0.82), Inches(0.72), Inches(0.04))
    line.fill.solid()
    line.fill.fore_color.rgb = accent
    line.line.fill.background()


def add_brand(slide, slide_no: int) -> None:
    badge = slide.shapes.add_shape(MSO_SHAPE.ROUNDED_RECTANGLE, Inches(0.75), Inches(0.28), Inches(1.7), Inches(0.38))
    badge.fill.solid()
    badge.fill.fore_color.rgb = PANEL_ALT
    badge.fill.transparency = 12
    badge.line.color.rgb = BLUE

    tf = badge.text_frame
    tf.clear()
    tf.vertical_anchor = MSO_ANCHOR.MIDDLE
    p = tf.paragraphs[0]
    p.alignment = PP_ALIGN.CENTER
    run = p.add_run()
    run.text = "VYRA"
    run.font.name = "Aptos"
    run.font.bold = True
    run.font.size = Pt(13)
    run.font.color.rgb = TEXT

    footer = slide.shapes.add_textbox(Inches(11.88), Inches(7.06), Inches(0.7), Inches(0.22))
    p = footer.text_frame.paragraphs[0]
    p.alignment = PP_ALIGN.RIGHT
    run = p.add_run()
    run.text = f"{slide_no:02d}"
    run.font.name = "Aptos"
    run.font.size = Pt(10)
    run.font.color.rgb = TEXT_SOFT


def add_text_box(
    slide,
    text: str,
    left,
    top,
    width,
    height,
    *,
    font_size: int,
    color: RGBColor = TEXT,
    bold: bool = False,
    name: str = "Aptos",
    align=PP_ALIGN.LEFT,
    valign=MSO_ANCHOR.TOP,
) -> None:
    textbox = slide.shapes.add_textbox(left, top, width, height)
    tf = textbox.text_frame
    tf.clear()
    tf.word_wrap = True
    tf.vertical_anchor = valign
    p = tf.paragraphs[0]
    p.alignment = align
    run = p.add_run()
    run.text = text
    run.font.name = name
    run.font.size = Pt(font_size)
    run.font.bold = bold
    run.font.color.rgb = color


def add_multiline_box(
    slide,
    lines: Iterable[tuple[str, dict]],
    left,
    top,
    width,
    height,
    *,
    valign=MSO_ANCHOR.TOP,
) -> None:
    textbox = slide.shapes.add_textbox(left, top, width, height)
    tf = textbox.text_frame
    tf.clear()
    tf.word_wrap = True
    tf.vertical_anchor = valign
    for idx, (text, style) in enumerate(lines):
        paragraph = tf.paragraphs[0] if idx == 0 else tf.add_paragraph()
        paragraph.alignment = style.get("align", PP_ALIGN.LEFT)
        paragraph.space_after = Pt(style.get("space_after", 3))
        run = paragraph.add_run()
        run.text = text
        run.font.name = style.get("name", "Aptos")
        run.font.size = Pt(style.get("font_size", 18))
        run.font.bold = style.get("bold", False)
        run.font.color.rgb = style.get("color", TEXT)


def add_panel(slide, left, top, width, height, *, line_color: RGBColor = BLUE, fill: RGBColor = PANEL, transparency: int = 10):
    panel = slide.shapes.add_shape(MSO_SHAPE.ROUNDED_RECTANGLE, left, top, width, height)
    panel.fill.solid()
    panel.fill.fore_color.rgb = fill
    panel.fill.transparency = transparency
    panel.line.color.rgb = line_color
    return panel


def add_chip(slide, text: str, left, top, width, accent: RGBColor) -> None:
    chip = slide.shapes.add_shape(MSO_SHAPE.ROUNDED_RECTANGLE, left, top, width, Inches(0.44))
    chip.fill.solid()
    chip.fill.fore_color.rgb = PANEL_ALT
    chip.fill.transparency = 8
    chip.line.color.rgb = accent

    tf = chip.text_frame
    tf.clear()
    tf.vertical_anchor = MSO_ANCHOR.MIDDLE
    p = tf.paragraphs[0]
    p.alignment = PP_ALIGN.CENTER
    run = p.add_run()
    run.text = text
    run.font.name = "Aptos"
    run.font.size = Pt(11)
    run.font.bold = True
    run.font.color.rgb = TEXT


def add_image_panel(
    slide,
    temp_dir: Path,
    source: Path,
    left,
    top,
    width,
    height,
    *,
    label: str | None = None,
    title: str | None = None,
    caption: str | None = None,
    accent: RGBColor = BLUE,
    brightness: float = 1.0,
) -> None:
    add_panel(slide, left, top, width, height, line_color=accent, fill=PANEL, transparency=5)
    pad = Inches(0.16)
    image_height = height - pad * 2
    text_block = Inches(0)

    if title or caption:
        text_block = Inches(0.82)
        image_height = height - pad * 3 - text_block

    image_path = prepare_cover_image(
        source,
        temp_dir / f"{source.stem}_{int(emu_to_inches(width) * 200)}x{int(emu_to_inches(height) * 200)}.png",
        int(emu_to_inches(width - pad * 2) * 220),
        int(emu_to_inches(image_height) * 220),
        brightness=brightness,
    )
    slide.shapes.add_picture(str(image_path), left + pad, top + pad, width=width - pad * 2, height=image_height)

    if label:
        pill = slide.shapes.add_shape(MSO_SHAPE.ROUNDED_RECTANGLE, left + Inches(0.26), top + Inches(0.24), Inches(1.5), Inches(0.34))
        pill.fill.solid()
        pill.fill.fore_color.rgb = BG
        pill.fill.transparency = 12
        pill.line.color.rgb = accent
        tf = pill.text_frame
        tf.clear()
        tf.vertical_anchor = MSO_ANCHOR.MIDDLE
        p = tf.paragraphs[0]
        p.alignment = PP_ALIGN.CENTER
        run = p.add_run()
        run.text = label
        run.font.name = "Aptos"
        run.font.size = Pt(10.5)
        run.font.bold = True
        run.font.color.rgb = TEXT

    if title or caption:
        text_left = left + Inches(0.2)
        text_top = top + pad + image_height + Inches(0.08)
        if title:
            add_text_box(slide, title, text_left, text_top, width - Inches(0.4), Inches(0.32), font_size=13, bold=True)
            text_top += Inches(0.28)
        if caption:
            add_text_box(slide, caption, text_left, text_top, width - Inches(0.4), Inches(0.42), font_size=9, color=TEXT_SOFT)


def add_title_block(slide, eyebrow: str, title: str, subtitle: str, *, accent: RGBColor, left, top, width) -> None:
    add_text_box(slide, eyebrow.upper(), left, top, width, Inches(0.26), font_size=10, color=accent, bold=True)
    add_text_box(slide, title, left, top + Inches(0.34), width, Inches(1.45), font_size=26, bold=True)
    add_text_box(slide, subtitle, left, top + Inches(1.62), width, Inches(0.95), font_size=12, color=TEXT_MUTED)


def add_problem_card(slide, left, top, title: str, body: str, accent: RGBColor) -> None:
    add_panel(slide, left, top, Inches(2.85), Inches(1.4), line_color=accent, fill=PANEL_ALT, transparency=12)
    add_text_box(slide, title, left + Inches(0.18), top + Inches(0.16), Inches(2.5), Inches(0.34), font_size=13, bold=True)
    add_text_box(slide, body, left + Inches(0.18), top + Inches(0.58), Inches(2.45), Inches(0.58), font_size=9, color=TEXT_SOFT)


def add_capability_card(slide, left, top, title: str, body: str, accent: RGBColor) -> None:
    add_panel(slide, left, top, Inches(3.0), Inches(3.1), line_color=accent, fill=PANEL, transparency=8)
    bar = slide.shapes.add_shape(MSO_SHAPE.RECTANGLE, left + Inches(0.18), top + Inches(0.18), Inches(0.7), Inches(0.05))
    bar.fill.solid()
    bar.fill.fore_color.rgb = accent
    bar.line.fill.background()
    add_text_box(slide, title, left + Inches(0.18), top + Inches(0.34), Inches(2.5), Inches(0.52), font_size=16, bold=True)
    add_text_box(slide, body, left + Inches(0.18), top + Inches(0.92), Inches(2.58), Inches(1.3), font_size=10, color=TEXT_SOFT)


def add_use_case_card(slide, left, top, title: str, body: str, accent: RGBColor) -> None:
    add_panel(slide, left, top, Inches(2.9), Inches(2.0), line_color=accent, fill=PANEL, transparency=8)
    add_text_box(slide, title, left + Inches(0.18), top + Inches(0.18), Inches(2.35), Inches(0.34), font_size=14, bold=True)
    add_text_box(slide, body, left + Inches(0.18), top + Inches(0.6), Inches(2.45), Inches(0.92), font_size=9, color=TEXT_SOFT)


def add_trust_card(slide, left, top, width, title: str, body: str, accent: RGBColor) -> None:
    add_panel(slide, left, top, width, Inches(0.86), line_color=accent, fill=PANEL_ALT, transparency=8)
    dot = slide.shapes.add_shape(MSO_SHAPE.OVAL, left + Inches(0.18), top + Inches(0.22), Inches(0.18), Inches(0.18))
    dot.fill.solid()
    dot.fill.fore_color.rgb = accent
    dot.line.fill.background()
    add_text_box(slide, title, left + Inches(0.48), top + Inches(0.12), width - Inches(0.7), Inches(0.22), font_size=11, bold=True)
    add_text_box(slide, body, left + Inches(0.48), top + Inches(0.38), width - Inches(0.7), Inches(0.26), font_size=8.5, color=TEXT_SOFT)


def add_pipeline_node(slide, left, top, title: str, body: str, accent: RGBColor) -> None:
    add_panel(slide, left, top, Inches(2.6), Inches(0.92), line_color=accent, fill=PANEL, transparency=10)
    add_text_box(slide, title, left + Inches(0.16), top + Inches(0.12), Inches(2.2), Inches(0.24), font_size=12, bold=True)
    add_text_box(slide, body, left + Inches(0.16), top + Inches(0.42), Inches(2.24), Inches(0.24), font_size=8.5, color=TEXT_SOFT)


def add_timeline_step(slide, left, top, step_no: str, title: str, body: str, accent: RGBColor) -> None:
    circle = slide.shapes.add_shape(MSO_SHAPE.OVAL, left, top, Inches(0.44), Inches(0.44))
    circle.fill.solid()
    circle.fill.fore_color.rgb = accent
    circle.line.fill.background()
    tf = circle.text_frame
    tf.clear()
    tf.vertical_anchor = MSO_ANCHOR.MIDDLE
    p = tf.paragraphs[0]
    p.alignment = PP_ALIGN.CENTER
    r = p.add_run()
    r.text = step_no
    r.font.name = "Aptos"
    r.font.bold = True
    r.font.size = Pt(11)
    r.font.color.rgb = BG

    add_panel(slide, left + Inches(0.58), top - Inches(0.04), Inches(4.85), Inches(0.78), line_color=accent, fill=PANEL, transparency=9)
    add_text_box(slide, title, left + Inches(0.76), top + Inches(0.02), Inches(2.8), Inches(0.18), font_size=11.5, bold=True)
    add_text_box(slide, body, left + Inches(0.76), top + Inches(0.26), Inches(4.35), Inches(0.24), font_size=8.5, color=TEXT_SOFT)


def slide_title(prs: Presentation, temp_dir: Path) -> None:
    slide = prs.slides.add_slide(prs.slide_layouts[6])
    add_background(slide, BLUE)
    add_brand(slide, 1)

    add_multiline_box(
        slide,
        [
            ("CLIENT DEMO DECK", {"font_size": 10, "bold": True, "color": CYAN, "space_after": 4}),
            ("VYRA", {"font_size": 30, "bold": True, "color": TEXT, "space_after": 0}),
            ("See everything.\nMiss nothing.", {"font_size": 22, "bold": True, "color": TEXT, "space_after": 8}),
            (
                "An AI command surface for safety, compliance, investigation, and response built on top of existing camera infrastructure.",
                {"font_size": 12, "color": TEXT_MUTED, "space_after": 0},
            ),
        ],
        Inches(0.75),
        Inches(1.02),
        Inches(5.45),
        Inches(3.0),
    )

    add_chip(slide, "Real-time detection", Inches(0.75), Inches(5.2), Inches(1.95), CYAN)
    add_chip(slide, "Evidence-linked review", Inches(2.92), Inches(5.2), Inches(2.18), BLUE)
    add_chip(slide, "On-prem or air-gapped", Inches(5.32), Inches(5.2), Inches(2.28), MINT)

    add_text_box(
        slide,
        "Built for teams that need operational clarity from live environments, not another disconnected alert feed.",
        Inches(0.75),
        Inches(5.82),
        Inches(5.95),
        Inches(0.65),
        font_size=10,
        color=TEXT_SOFT,
    )

    add_image_panel(
        slide,
        temp_dir,
        PRODUCT / "control-agents-deep.webp",
        Inches(7.02),
        Inches(0.84),
        Inches(5.55),
        Inches(5.78),
        label="Command Surface",
        title="Command center, investigation, analytics, and Ask AI in one product story.",
        caption="The deck uses current VYRA product visuals from the application repo.",
        accent=CYAN,
    )


def slide_problem(prs: Presentation, temp_dir: Path) -> None:
    slide = prs.slides.add_slide(prs.slide_layouts[6])
    add_background(slide, RED)
    add_brand(slide, 2)
    add_title_block(
        slide,
        "The gap",
        "The problem is not camera coverage.\nIt is operational meaning.",
        "Most sites already have video. What they do not have is a system that can surface risk, keep evidence attached, and help teams respond fast.",
        accent=RED,
        left=Inches(0.75),
        top=Inches(0.88),
        width=Inches(5.75),
    )

    problems = [
        ("Too many feeds", "Operators watch walls of video but still miss what deserves attention.", RED),
        ("Low-context alerts", "Traditional alerts often arrive without identity, severity, or supporting evidence.", AMBER),
        ("Slow investigations", "Teams jump between footage, screenshots, and notes just to reconstruct one incident.", CYAN),
        ("Fragmented workflows", "Detection, review, reporting, and escalation often live in separate tools.", MINT),
    ]
    positions = [
        (Inches(0.75), Inches(2.55)),
        (Inches(3.8), Inches(2.55)),
        (Inches(0.75), Inches(4.12)),
        (Inches(3.8), Inches(4.12)),
    ]
    for (title, body, accent), (left, top) in zip(problems, positions):
        add_problem_card(slide, left, top, title, body, accent)

    add_image_panel(
        slide,
        temp_dir,
        SHOTS / "operator-active.webp",
        Inches(7.15),
        Inches(1.12),
        Inches(5.35),
        Inches(5.55),
        label="Operator Reality",
        title="Visibility alone does not equal faster response.",
        caption="VYRA is positioned to turn passive monitoring into a guided operational workflow.",
        accent=RED,
        brightness=0.94,
    )


def slide_solution(prs: Presentation, temp_dir: Path) -> None:
    slide = prs.slides.add_slide(prs.slide_layouts[6])
    add_background(slide, CYAN)
    add_brand(slide, 3)
    add_title_block(
        slide,
        "Solution",
        "VYRA becomes the operating layer above existing CCTV.",
        "Instead of replacing camera infrastructure, VYRA connects live detection, investigation, coordination, and explanation inside one operator-ready interface.",
        accent=CYAN,
        left=Inches(0.75),
        top=Inches(0.88),
        width=Inches(11.9),
    )

    cards = [
        ("Detect", "Live PPE, person, and situational signals are surfaced from the same feed.", CYAN),
        ("Investigate", "Events keep source frames, summaries, and identity context attached to the same record.", BLUE),
        ("Coordinate", "Operators manage cameras, workers, and system health from one command center.", MINT),
        ("Explain", "Ask AI makes incident search and evidence retrieval accessible in natural language.", AMBER),
    ]
    x_positions = [Inches(0.75), Inches(3.93), Inches(7.11), Inches(10.29)]
    for (title, body, accent), left in zip(cards, x_positions):
        add_capability_card(slide, left, Inches(2.3), title, body, accent)

    add_panel(slide, Inches(0.75), Inches(5.82), Inches(11.9), Inches(0.74), line_color=BLUE, fill=PANEL_ALT, transparency=10)
    add_text_box(
        slide,
        "Business message: VYRA helps clients act on risk sooner, investigate with confidence, and get more value from the camera estate they already own.",
        Inches(0.98),
        Inches(6.02),
        Inches(8.35),
        Inches(0.22),
        font_size=10,
        color=TEXT_MUTED,
    )
    add_image_panel(
        slide,
        temp_dir,
        PRODUCT / "event-card.webp",
        Inches(9.5),
        Inches(5.9),
        Inches(2.95),
        Inches(0.56),
        accent=BLUE,
    )


def slide_workflows(prs: Presentation, temp_dir: Path) -> None:
    slide = prs.slides.add_slide(prs.slide_layouts[6])
    add_background(slide, BLUE)
    add_brand(slide, 4)
    add_title_block(
        slide,
        "Product story",
        "What the client actually sees in the platform.",
        "The commercial narrative is strongest when clients can immediately picture the operator experience.",
        accent=BLUE,
        left=Inches(0.75),
        top=Inches(0.88),
        width=Inches(11.8),
    )

    workflow_cards = [
        ("Command Center", PRODUCT / "control-agents.webp", "Live camera control, agent assignment, and system visibility.", CYAN),
        ("Investigate", PRODUCT / "investigate-events.webp", "Event timelines with source evidence, summaries, and operator notes.", MINT),
        ("Analytics", PRODUCT / "analytics-overview.webp", "Trend views that make compliance drift and risk clusters visible.", AMBER),
        ("Ask AI", PRODUCT / "ask-ai.webp", "Natural-language incident search with evidence-backed answers.", RED),
    ]
    slots = [
        (Inches(0.75), Inches(1.9)),
        (Inches(6.75), Inches(1.9)),
        (Inches(0.75), Inches(4.42)),
        (Inches(6.75), Inches(4.42)),
    ]
    for (title, image, caption, accent), (left, top) in zip(workflow_cards, slots):
        add_image_panel(
            slide,
            temp_dir,
            image,
            left,
            top,
            Inches(5.8),
            Inches(2.26),
            label=title,
            title=title,
            caption=caption,
            accent=accent,
        )


def slide_use_cases(prs: Presentation, temp_dir: Path) -> None:
    slide = prs.slides.add_slide(prs.slide_layouts[6])
    add_background(slide, AMBER)
    add_brand(slide, 5)
    add_title_block(
        slide,
        "Use cases",
        "Designed for high-visibility industries.",
        "VYRA already frames its story around environments where safety, uptime, and evidence confidence matter every day.",
        accent=AMBER,
        left=Inches(0.75),
        top=Inches(0.88),
        width=Inches(5.4),
    )

    add_image_panel(
        slide,
        temp_dir,
        SHOTS / "ops-floor.webp",
        Inches(0.75),
        Inches(2.05),
        Inches(5.25),
        Inches(4.25),
        label="Operational Environment",
        title="Built for active sites where blind spots carry real cost.",
        caption="The pitch is strongest with buyers in manufacturing, warehousing, construction, and critical infrastructure.",
        accent=AMBER,
        brightness=0.92,
    )

    use_cases = [
        ("Manufacturing", "Monitor PPE compliance, unsafe movement, and workstation blind spots across production floors.", CYAN),
        ("Warehousing", "Track people, perimeter zones, and workflow violations across dense camera layouts.", BLUE),
        ("Construction", "Surface missing safety gear and incident-ready evidence in constantly changing sites.", RED),
        ("Critical Infrastructure", "Support high-trust monitoring where response certainty and evidence retention are non-negotiable.", MINT),
    ]
    slots = [
        (Inches(6.32), Inches(2.0)),
        (Inches(9.36), Inches(2.0)),
        (Inches(6.32), Inches(4.18)),
        (Inches(9.36), Inches(4.18)),
    ]
    for (title, body, accent), (left, top) in zip(use_cases, slots):
        add_use_case_card(slide, left, top, title, body, accent)

    add_panel(slide, Inches(0.75), Inches(6.45), Inches(11.9), Inches(0.58), line_color=AMBER, fill=PANEL_ALT, transparency=12)
    add_text_box(
        slide,
        "Typical commercial triggers: compliance audits, recurring incidents, slow evidence review, fragmented control rooms, or the need to modernize without replacing cameras.",
        Inches(1.0),
        Inches(6.62),
        Inches(11.35),
        Inches(0.2),
        font_size=9.5,
        color=TEXT_MUTED,
    )


def slide_trust(prs: Presentation, temp_dir: Path) -> None:
    slide = prs.slides.add_slide(prs.slide_layouts[6])
    add_background(slide, MINT)
    add_brand(slide, 6)
    add_title_block(
        slide,
        "Trust and deployment",
        "Built for serious operating environments.",
        "This is the non-technical trust story: existing camera compatibility, controlled deployment models, secure access, and evidence-linked review.",
        accent=MINT,
        left=Inches(0.75),
        top=Inches(0.88),
        width=Inches(11.9),
    )

    add_text_box(slide, "Operational pipeline", Inches(0.75), Inches(1.92), Inches(2.2), Inches(0.2), font_size=11, bold=True, color=CYAN)
    pipeline_left = Inches(0.75)
    pipeline_top = Inches(2.22)
    pipeline = [
        ("Cameras", "RTSP or edge-connected feeds"),
        ("Secure relay", "Orchestrates ingest, streaming, and jobs"),
        ("AI analysis", "Detection, summaries, and identity context"),
        ("Dashboard", "Review, investigate, and respond"),
    ]
    accents = [CYAN, BLUE, MINT, AMBER]
    current_top = pipeline_top
    for idx, ((title, body), accent) in enumerate(zip(pipeline, accents)):
        add_pipeline_node(slide, pipeline_left, current_top, title, body, accent)
        if idx < len(pipeline) - 1:
            connector = slide.shapes.add_shape(MSO_SHAPE.RECTANGLE, pipeline_left + Inches(1.25), current_top + Inches(0.94), Inches(0.08), Inches(0.3))
            connector.fill.solid()
            connector.fill.fore_color.rgb = accent
            connector.fill.transparency = 20
            connector.line.fill.background()
        current_top += Inches(1.22)

    add_image_panel(
        slide,
        temp_dir,
        PRODUCT / "daily-brief.webp",
        Inches(3.65),
        Inches(2.2),
        Inches(2.55),
        Inches(3.95),
        label="Daily Brief",
        title="A readable handoff for shifts and reviews.",
        caption="Daily summaries and evidence-linked records help position VYRA as operational software, not just a detector.",
        accent=CYAN,
    )

    trust_items = [
        ("Works with current camera estates", "The system is designed around live RTSP and edge-connected video sources.", CYAN),
        ("On-prem and air-gapped paths", "The repo includes both local production and air-gapped deployment guidance.", MINT),
        ("Authenticated browser access", "User login and token-based access are part of the platform flow.", BLUE),
        ("Evidence stays attached", "Events, snapshots, identities, and summaries stay tied to the same review chain.", AMBER),
        ("Scales with camera growth", "Worker pools and deployment controls are built for expanding coverage.", RED),
    ]
    right_left = Inches(6.65)
    start_top = Inches(2.04)
    for idx, (title, body, accent) in enumerate(trust_items):
        add_trust_card(slide, right_left, start_top + Inches(idx * 0.93), Inches(5.7), title, body, accent)

    add_panel(slide, Inches(6.65), Inches(6.72), Inches(5.7), Inches(0.42), line_color=MINT, fill=PANEL_ALT, transparency=10)
    add_text_box(
        slide,
        "Positioning line: built for live environments, not lab demos.",
        Inches(6.9),
        Inches(6.83),
        Inches(5.2),
        Inches(0.16),
        font_size=9.5,
        color=TEXT_MUTED,
    )


def slide_demo_flow(prs: Presentation, temp_dir: Path) -> None:
    slide = prs.slides.add_slide(prs.slide_layouts[6])
    add_background(slide, CYAN)
    add_brand(slide, 7)
    add_title_block(
        slide,
        "Demo flow",
        "A simple 12-15 minute live client walkthrough.",
        "Use the deck to set expectations, then show the product in the same sequence so the story feels guided and commercial.",
        accent=CYAN,
        left=Inches(0.75),
        top=Inches(0.88),
        width=Inches(11.8),
    )

    steps = [
        ("1", "Open the command center", "Show live camera coverage, camera groups, and system readiness.", CYAN),
        ("2", "Surface one incident", "Use a PPE or situational event to anchor the story in a real operational moment.", RED),
        ("3", "Investigate the timeline", "Open the event record to show snapshots, notes, summary, and supporting context.", BLUE),
        ("4", "Show trends and the daily brief", "Move from one event to the broader operational picture for managers.", AMBER),
        ("5", "Finish with Ask AI", "Query the incident in natural language to highlight accessibility and speed.", MINT),
    ]
    top = Inches(2.0)
    for step_no, title, body, accent in steps:
        add_timeline_step(slide, Inches(0.75), top, step_no, title, body, accent)
        top += Inches(0.93)

    add_image_panel(
        slide,
        temp_dir,
        PRODUCT / "investigate-timeline.webp",
        Inches(7.0),
        Inches(1.95),
        Inches(5.45),
        Inches(4.42),
        label="Suggested Live View",
        title="Investigation is usually the emotional center of the demo.",
        caption="This is where clients see that alerts, evidence, summaries, and action context stay together.",
        accent=BLUE,
    )

    add_panel(slide, Inches(7.0), Inches(6.52), Inches(5.45), Inches(0.56), line_color=CYAN, fill=PANEL_ALT, transparency=10)
    add_text_box(
        slide,
        "Tip: keep the narration outcome-focused. Lead with faster response, clearer evidence, and easier operator adoption.",
        Inches(7.22),
        Inches(6.7),
        Inches(5.02),
        Inches(0.18),
        font_size=9.2,
        color=TEXT_MUTED,
    )


def slide_close(prs: Presentation, temp_dir: Path) -> None:
    slide = prs.slides.add_slide(prs.slide_layouts[6])
    add_background(slide, BLUE)
    add_brand(slide, 8)
    add_title_block(
        slide,
        "Commercial takeaway",
        "Why VYRA resonates with clients.",
        "The close should connect the product story back to business value and next-step urgency.",
        accent=BLUE,
        left=Inches(0.75),
        top=Inches(0.88),
        width=Inches(11.8),
    )

    outcomes = [
        ("Safer operations", "Surface PPE misses, suspicious activity, and review-ready incidents before they turn into larger failures.", CYAN),
        ("Faster investigations", "Keep the event, the evidence, and the explanation attached so teams can move quickly with confidence.", MINT),
        ("More value from existing cameras", "Modernize the operating layer without forcing a full replacement of current video infrastructure.", AMBER),
    ]
    lefts = [Inches(0.75), Inches(4.72), Inches(8.69)]
    for (title, body, accent), left in zip(outcomes, lefts):
        add_panel(slide, left, Inches(2.3), Inches(3.55), Inches(2.55), line_color=accent, fill=PANEL, transparency=8)
        add_text_box(slide, title, left + Inches(0.2), Inches(2.54), Inches(2.95), Inches(0.36), font_size=16, bold=True)
        add_text_box(slide, body, left + Inches(0.2), Inches(3.05), Inches(3.02), Inches(1.15), font_size=10, color=TEXT_SOFT)

    add_image_panel(
        slide,
        temp_dir,
        PRODUCT / "control-cameras.webp",
        Inches(0.75),
        Inches(5.18),
        Inches(5.25),
        Inches(1.58),
        label="Final Proof Point",
        title="Serious software for serious monitoring teams.",
        caption="Use the live product next to this close to move naturally into follow-up questions and deployment conversations.",
        accent=CYAN,
    )

    add_panel(slide, Inches(6.28), Inches(5.18), Inches(6.37), Inches(1.58), line_color=BLUE, fill=PANEL_ALT, transparency=8)
    add_multiline_box(
        slide,
        [
            ("Best-fit buyers", {"font_size": 11, "bold": True, "color": CYAN, "space_after": 4}),
            (
                "Safety leaders, operations heads, plant managers, compliance teams, and security stakeholders across manufacturing, warehousing, construction, and critical infrastructure.",
                {"font_size": 10, "color": TEXT_MUTED, "space_after": 6},
            ),
            ("Recommended next step", {"font_size": 11, "bold": True, "color": MINT, "space_after": 4}),
            ("Lead from this deck into a live walkthrough using the same command center -> incident -> investigate -> analytics -> Ask AI sequence.", {"font_size": 10, "color": TEXT_MUTED}),
        ],
        Inches(6.54),
        Inches(5.38),
        Inches(5.9),
        Inches(1.0),
    )


def build_presentation(output_path: Path) -> Path:
    output_path.parent.mkdir(parents=True, exist_ok=True)
    prs = Presentation()
    prs.slide_width = SLIDE_W
    prs.slide_height = SLIDE_H
    prs.core_properties.title = "VYRA Client Demo Pitch Deck"
    prs.core_properties.subject = "Client-facing overview of VYRA"
    prs.core_properties.author = "Codex"
    prs.core_properties.company = "Hexmon Technology"
    prs.core_properties.comments = "Generated from current repo documentation and marketing assets."

    with tempfile.TemporaryDirectory(prefix="vyra-pitch-") as temp_name:
        temp_dir = Path(temp_name)
        slide_title(prs, temp_dir)
        slide_problem(prs, temp_dir)
        slide_solution(prs, temp_dir)
        slide_workflows(prs, temp_dir)
        slide_use_cases(prs, temp_dir)
        slide_trust(prs, temp_dir)
        slide_demo_flow(prs, temp_dir)
        slide_close(prs, temp_dir)

    prs.save(str(output_path))
    return output_path


def main() -> int:
    parser = argparse.ArgumentParser(description="Generate the VYRA client demo deck.")
    parser.add_argument("--output", type=Path, default=DEFAULT_OUTPUT, help="Path to the output .pptx file")
    args = parser.parse_args()
    output = build_presentation(args.output.resolve())
    print(output)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
