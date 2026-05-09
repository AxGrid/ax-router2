/** @type {import('tailwindcss').Config} */
//
// Colors, radii and fonts come from Ax Styler tokens (see src/styles/tokens.css).
// We expose them under Tailwind class names so existing utility usage stays
// idiomatic — `bg-bg`, `text-fg`, `text-fg-muted`, `border-border`,
// `text-brand-300`, etc. Opacity modifiers (`bg-foo/40`) won't work on these
// because Tailwind v3's <alpha-value> requires rgb-style colors; for those
// cases use `color-mix` directly inside a component class in index.css.
export default {
  content: ['./index.html', './src/**/*.{ts,tsx}'],
  theme: {
    extend: {
      fontFamily: {
        sans: ['Inter', 'ui-sans-serif', 'system-ui', 'sans-serif'],
        mono: ['JetBrains Mono', 'ui-monospace', 'monospace'],
      },
      colors: {
        bg: 'var(--bg)',
        'bg-elevated': 'var(--bg-elevated)',
        surface: 'var(--surface)',
        'surface-2': 'var(--surface-2)',
        'surface-hover': 'var(--surface-hover)',
        fg: 'var(--fg)',
        'fg-muted': 'var(--fg-muted)',
        'fg-subtle': 'var(--fg-subtle)',
        border: 'var(--border)',
        'border-strong': 'var(--border-strong)',
        accent: 'var(--accent)',
        'accent-hover': 'var(--accent-hover)',
        'accent-fg': 'var(--accent-fg)',
        brand: {
          50:  'var(--brand-50)',
          100: 'var(--brand-100)',
          200: 'var(--brand-200)',
          300: 'var(--brand-300)',
          400: 'var(--brand-400)',
          500: 'var(--brand-500)',
          600: 'var(--brand-600)',
          700: 'var(--brand-700)',
          800: 'var(--brand-800)',
          900: 'var(--brand-900)',
        },
        success: 'var(--success)',
        warning: 'var(--warning)',
        danger: 'var(--danger)',
      },
      // Bare `border` should pick up our themed border token.
      borderColor: {
        DEFAULT: 'var(--border)',
      },
      borderRadius: {
        xs: 'var(--radius-xs)',
        sm: 'var(--radius-sm)',
        md: 'var(--radius-md)',
        lg: 'var(--radius-lg)',
        xl: 'var(--radius-xl)',
        '2xl': 'var(--radius-2xl)',
      },
    },
  },
  plugins: [],
};
