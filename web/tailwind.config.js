/** @type {import('tailwindcss').Config} */
export default {
  content: ['./index.html', './src/**/*.{ts,tsx}'],
  theme: {
    extend: {
      fontFamily: {
        sans: ['Inter', 'ui-sans-serif', 'system-ui', 'sans-serif'],
        mono: ['JetBrains Mono', 'ui-monospace', 'monospace'],
      },
      colors: {
        ink: {
          950: '#08080a',
          900: '#0a0a0a',
          800: '#15141a',
          700: '#1c1b22',
          600: '#26252e',
          500: '#3a3845',
          400: '#5b5867',
        },
        flame: {
          50: '#fff7ed',
          100: '#ffedd5',
          200: '#fed7aa',
          300: '#fdba74',
          400: '#fb923c',
          500: '#f97316',
          600: '#ea580c',
          700: '#c2410c',
          800: '#9a3412',
          900: '#7c2d12',
        },
      },
      boxShadow: {
        glow: '0 0 30px rgba(249,115,22,0.18)',
      },
    },
  },
  plugins: [],
};
