use ratatui::style::{Color, Style};

/// Semantic color roles shared by every TUI surface. Text uses paired
/// foreground/background values so fixed light and dark themes both stay
/// readable. The derived native theme keeps body text on the terminal's own
/// foreground (`Color::Reset`) and only derives surfaces and accents from the
/// sampled default colors.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct Theme {
    pub text: Color,
    pub secondary: Color,
    pub accent: Color,
    pub rule: Color,
    pub user_fg: Color,
    pub user_bg: Color,
    pub code: Color,
    pub warning: Color,
    pub error: Color,
}

/// How the palette is chosen. `Auto` starts on the native fallback and is
/// updated from terminal color samples; manual settings never change.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum ThemeSetting {
    Auto,
    Light,
    Dark,
}

impl ThemeSetting {
    pub fn parse(value: Option<&str>) -> Self {
        match value {
            Some("light") => Self::Light,
            Some("dark") => Self::Dark,
            _ => Self::Auto,
        }
    }

    pub fn from_env() -> Self {
        Self::parse(std::env::var("SAKI_THEME").ok().as_deref())
    }

    /// The palette used before (and, without detection, instead of) a detected
    /// terminal appearance.
    pub fn initial_theme(self) -> Theme {
        match self {
            Self::Light => Theme::light(),
            Self::Dark => Theme::dark(),
            Self::Auto => Theme::fallback(),
        }
    }
}

/// Terminal default foreground/background sampled with OSC 10/11.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct NativeColors {
    pub foreground: (u8, u8, u8),
    pub background: (u8, u8, u8),
}

type Rgb = (u8, u8, u8);

/// Minimum contrast ratio for every text-bearing role on the surface it is
/// actually rendered on.
pub(crate) const MIN_TEXT_CONTRAST: f64 = 4.5;

fn relative_luminance((red, green, blue): Rgb) -> f64 {
    let channel = |value: u8| {
        let value = f64::from(value) / 255.0;
        if value <= 0.039_28 {
            value / 12.92
        } else {
            ((value + 0.055) / 1.055).powf(2.4)
        }
    };
    0.2126 * channel(red) + 0.7152 * channel(green) + 0.0722 * channel(blue)
}

pub(crate) fn contrast_rgb(first: Rgb, second: Rgb) -> f64 {
    let first = relative_luminance(first);
    let second = relative_luminance(second);
    let (light, dark) = if first >= second {
        (first, second)
    } else {
        (second, first)
    };
    (light + 0.05) / (dark + 0.05)
}

fn blend(over: Rgb, under: Rgb, alpha: f64) -> Rgb {
    let mix = |over: u8, under: u8| {
        (f64::from(over) * alpha + f64::from(under) * (1.0 - alpha)).round() as u8
    };
    (
        mix(over.0, under.0),
        mix(over.1, under.1),
        mix(over.2, under.2),
    )
}

fn is_light(background: Rgb) -> bool {
    relative_luminance(background) >= 0.4
}

/// The most dimmed blend of `foreground` over `background` that still meets the
/// text contrast floor on both the terminal background and the user surface.
fn dim_until_readable(foreground: Rgb, background: Rgb, user_bg: Rgb) -> Rgb {
    let mut alpha = 0.5;
    while alpha < 1.0 {
        let candidate = blend(foreground, background, alpha);
        if contrast_rgb(candidate, background) >= MIN_TEXT_CONTRAST
            && contrast_rgb(candidate, user_bg) >= MIN_TEXT_CONTRAST
        {
            return candidate;
        }
        alpha += 0.05;
    }
    foreground
}

/// Picks a role color that keeps its semantic hue while meeting the contrast
/// floor on both the terminal background and the derived user surface. The
/// seed is tried first (`keep = 1.0`); it only mixes toward the terminal
/// foreground when contrast requires it.
fn ensure_readable(seed: Rgb, background: Rgb, user_bg: Rgb, foreground: Rgb) -> Rgb {
    let mut keep = 1.0;
    while keep >= 0.0 {
        let candidate = blend(seed, foreground, keep);
        if contrast_rgb(candidate, background) >= MIN_TEXT_CONTRAST
            && contrast_rgb(candidate, user_bg) >= MIN_TEXT_CONTRAST
        {
            return candidate;
        }
        keep -= 0.1;
    }
    foreground
}

fn color(rgb: Rgb) -> Color {
    Color::Rgb(rgb.0, rgb.1, rgb.2)
}

impl Theme {
    pub const fn dark() -> Self {
        Self {
            text: Color::Rgb(0xdc, 0xe4, 0xde),
            secondary: Color::Rgb(0x9a, 0xa9, 0xa0),
            accent: Color::Rgb(0x8b, 0xbd, 0xa2),
            rule: Color::Rgb(0x3b, 0x48, 0x40),
            user_fg: Color::Rgb(0xdc, 0xe4, 0xde),
            user_bg: Color::Rgb(0x27, 0x39, 0x2f),
            code: Color::Rgb(0x86, 0xb8, 0xd6),
            warning: Color::Rgb(0xd9, 0xb6, 0x74),
            error: Color::Rgb(0xe3, 0x97, 0x92),
        }
    }

    pub const fn light() -> Self {
        Self {
            text: Color::Rgb(0x29, 0x33, 0x2f),
            secondary: Color::Rgb(0x62, 0x6e, 0x67),
            accent: Color::Rgb(0x32, 0x63, 0x4e),
            rule: Color::Rgb(0xc8, 0xd2, 0xca),
            user_fg: Color::Rgb(0x29, 0x33, 0x2f),
            user_bg: Color::Rgb(0xe8, 0xee, 0xe7),
            code: Color::Rgb(0x2c, 0x5d, 0x7a),
            warning: Color::Rgb(0x82, 0x5e, 0x20),
            error: Color::Rgb(0xa3, 0x3d, 0x3d),
        }
    }

    /// Terminal-default fallback: body text uses the terminal's own foreground,
    /// and the user rectangle keeps an explicit paired green surface so it
    /// stays readable without guessing the terminal background.
    pub const fn fallback() -> Self {
        Self {
            text: Color::Reset,
            secondary: Color::Reset,
            accent: Color::Reset,
            rule: Color::Reset,
            user_fg: Color::Rgb(0xdc, 0xe4, 0xde),
            user_bg: Color::Rgb(0x27, 0x39, 0x2f),
            code: Color::Reset,
            warning: Color::Reset,
            error: Color::Reset,
        }
    }

    /// Selection highlight. Paired palettes use the user surface on accent; the
    /// native fallback keeps a known readable pair instead of dark text on an
    /// unknown default background.
    pub fn selection(self) -> Style {
        if self.accent == Color::Reset {
            Theme::dark().selection()
        } else {
            Style::default().fg(self.user_bg).bg(self.accent)
        }
    }
}

/// Derives a palette from the terminal's actual default foreground/background.
/// Body text always stays `Color::Reset` so the terminal foreground is
/// inherited; surfaces and accents are blended and contrast-checked against the
/// sampled background. Falls back when the surface pair cannot stay readable.
pub fn native_theme(colors: NativeColors) -> Theme {
    let foreground = colors.foreground;
    let background = colors.background;
    // A faint green tint over the actual background keeps the user rectangle
    // recognisable on any terminal without guessing a fixed surface.
    let user_tint = if is_light(background) {
        (0x2f, 0x6b, 0x52)
    } else {
        (0x8b, 0xbd, 0xa2)
    };
    let mut user_bg = blend(user_tint, background, 0.10);
    if contrast_rgb(foreground, user_bg) < MIN_TEXT_CONTRAST {
        user_bg = blend(user_tint, background, 0.05);
    }
    if contrast_rgb(foreground, user_bg) < MIN_TEXT_CONTRAST {
        return Theme::fallback();
    }
    let (accent_base, code_base, warning_base, error_base) = if is_light(background) {
        (
            (0x2f, 0x6b, 0x52),
            (0x2c, 0x5d, 0x7a),
            (0x7a, 0x57, 0x1c),
            (0x9e, 0x35, 0x35),
        )
    } else {
        (
            (0x8b, 0xbd, 0xa2),
            (0x86, 0xb8, 0xd6),
            (0xd9, 0xb6, 0x74),
            (0xe3, 0x97, 0x92),
        )
    };
    Theme {
        text: Color::Reset,
        secondary: color(dim_until_readable(foreground, background, user_bg)),
        accent: color(ensure_readable(
            accent_base,
            background,
            user_bg,
            foreground,
        )),
        rule: color(blend(foreground, background, 0.30)),
        user_fg: Color::Reset,
        user_bg: color(user_bg),
        code: color(ensure_readable(code_base, background, user_bg, foreground)),
        warning: color(ensure_readable(
            warning_base,
            background,
            user_bg,
            foreground,
        )),
        error: color(ensure_readable(error_base, background, user_bg, foreground)),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn rgb(color: Color) -> Rgb {
        match color {
            Color::Rgb(red, green, blue) => (red, green, blue),
            other => panic!("expected an RGB role, got {other:?}"),
        }
    }

    #[test]
    fn fixed_palettes_keep_all_roles_readable_on_their_backgrounds() {
        let cream = (0xf6, 0xef, 0xe2);
        let dark_background = (0x19, 0x1d, 0x1c);
        for (name, theme, background) in [
            ("light", Theme::light(), cream),
            ("dark", Theme::dark(), dark_background),
        ] {
            let roles = [
                ("text", theme.text),
                ("secondary", theme.secondary),
                ("code", theme.code),
                ("accent", theme.accent),
                ("warning", theme.warning),
                ("error", theme.error),
            ];
            for (role, color) in roles {
                assert!(
                    contrast_rgb(rgb(color), background) >= MIN_TEXT_CONTRAST,
                    "{name}: {role} must stay readable on its own background"
                );
            }
            assert!(contrast_rgb(rgb(theme.user_fg), rgb(theme.user_bg)) >= MIN_TEXT_CONTRAST);
        }
    }

    #[test]
    fn native_fallback_never_guesses_a_background() {
        let theme = Theme::fallback();
        for role in [
            theme.text,
            theme.secondary,
            theme.code,
            theme.warning,
            theme.error,
            theme.rule,
        ] {
            assert_eq!(role, Color::Reset);
        }
        assert!(contrast_rgb(rgb(theme.user_fg), rgb(theme.user_bg)) >= MIN_TEXT_CONTRAST);
        let selection = theme.selection();
        let (fg, bg) = (rgb(selection.fg.unwrap()), rgb(selection.bg.unwrap()));
        assert_ne!(selection.fg.unwrap(), Color::Reset);
        assert_ne!(selection.bg.unwrap(), Color::Reset);
        assert!(contrast_rgb(fg, bg) >= MIN_TEXT_CONTRAST);
    }

    #[test]
    fn sampled_colors_derive_readable_roles_and_inherit_body_text() {
        let cases: [(&str, Rgb, Rgb); 6] = [
            ("cream", (0x29, 0x33, 0x2f), (0xf6, 0xef, 0xe2)),
            ("dark", (0xdc, 0xe4, 0xde), (0x19, 0x1d, 0x1c)),
            ("medium gray", (0x00, 0x00, 0x00), (0x80, 0x80, 0x80)),
            ("saturated blue", (0xff, 0xff, 0xff), (0x00, 0x33, 0x66)),
            ("custom red", (0xff, 0xff, 0xff), (0x7a, 0x1f, 0x1f)),
            ("purple on cream", (0x57, 0x52, 0x79), (0xf6, 0xef, 0xe2)),
        ];
        for (name, foreground, background) in cases {
            let theme = native_theme(NativeColors {
                foreground,
                background,
            });
            assert_eq!(theme.text, Color::Reset, "{name}: body inherits foreground");
            assert_eq!(
                theme.user_fg,
                Color::Reset,
                "{name}: user text inherits foreground"
            );
            let user_bg = rgb(theme.user_bg);
            assert!(
                contrast_rgb(foreground, user_bg) >= MIN_TEXT_CONTRAST,
                "{name}: user surface keeps the terminal foreground readable"
            );
            for (role, color) in [
                ("secondary", theme.secondary),
                ("accent", theme.accent),
                ("code", theme.code),
                ("warning", theme.warning),
                ("error", theme.error),
            ] {
                assert!(
                    contrast_rgb(rgb(color), background) >= MIN_TEXT_CONTRAST,
                    "{name}: {role} on the sampled background"
                );
                assert!(
                    contrast_rgb(rgb(color), user_bg) >= MIN_TEXT_CONTRAST,
                    "{name}: {role} on the user surface"
                );
            }
            let selection = theme.selection();
            assert!(
                contrast_rgb(rgb(selection.fg.unwrap()), rgb(selection.bg.unwrap()))
                    >= MIN_TEXT_CONTRAST,
                "{name}: selection pair stays readable"
            );
        }
    }

    #[test]
    fn readable_seeds_keep_distinct_semantic_hues() {
        let cases: [(&str, Rgb, Rgb); 6] = [
            ("cream", (0x29, 0x33, 0x2f), (0xf6, 0xef, 0xe2)),
            ("dark", (0xdc, 0xe4, 0xde), (0x19, 0x1d, 0x1c)),
            ("purple terminal", (0xe0, 0xc8, 0xff), (0x1a, 0x0f, 0x2e)),
            ("saturated blue", (0xff, 0xff, 0xff), (0x00, 0x33, 0x66)),
            ("custom red", (0xff, 0xff, 0xff), (0x7a, 0x1f, 0x1f)),
            ("purple on cream", (0x57, 0x52, 0x79), (0xf6, 0xef, 0xe2)),
        ];
        for (name, foreground, background) in cases {
            let theme = native_theme(NativeColors {
                foreground,
                background,
            });
            let roles = [
                ("accent", rgb(theme.accent)),
                ("code", rgb(theme.code)),
                ("warning", rgb(theme.warning)),
                ("error", rgb(theme.error)),
            ];
            for (left, (left_name, left_rgb)) in roles.iter().enumerate() {
                assert_ne!(
                    *left_rgb, foreground,
                    "{name}: {left_name} must not collapse into the terminal foreground"
                );
                for (right_name, right_rgb) in &roles[left + 1..] {
                    assert_ne!(
                        left_rgb, right_rgb,
                        "{name}: {left_name} and {right_name} must stay distinguishable"
                    );
                }
            }
            assert_ne!(
                rgb(theme.accent),
                rgb(theme.code),
                "{name}: accent and code use different semantic hues"
            );
        }
    }

    #[test]
    fn unreadable_sampled_pair_falls_back_to_native_defaults() {
        // Foreground and background too close for a readable surface.
        let theme = native_theme(NativeColors {
            foreground: (0x80, 0x80, 0x80),
            background: (0x7a, 0x7a, 0x7a),
        });
        assert_eq!(theme, Theme::fallback());
    }
}
