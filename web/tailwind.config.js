/** @type {import('tailwindcss').Config} */
export default {
  content: ['./index.html', './src/**/*.{js,ts,jsx,tsx}'],
  theme: {
    extend: {
      colors: {
        void: '#0a0a1a',
        abyss: '#0f0f2e',
        neon: {
          cyan: '#00f0ff',
          magenta: '#ff00e5',
          purple: '#8b5cf6',
          pink: '#ff2d78',
        },
        terminal: {
          dim: '#1a1a3e',
          mid: '#252550',
          bright: '#3a3a6e',
        },
      },
      fontFamily: {
        display: ['Orbitron', 'monospace'],
        ui: ['Rajdhani', 'sans-serif'],
        mono: ['JetBrains Mono', 'monospace'],
      },
      boxShadow: {
        'neon-cyan': '0 0 5px #00f0ff, 0 0 20px rgba(0, 240, 255, 0.3), inset 0 0 5px rgba(0, 240, 255, 0.1)',
        'neon-magenta': '0 0 5px #ff00e5, 0 0 20px rgba(255, 0, 229, 0.3), inset 0 0 5px rgba(255, 0, 229, 0.1)',
        'neon-purple': '0 0 5px #8b5cf6, 0 0 20px rgba(139, 92, 246, 0.3)',
        'neon-pink': '0 0 5px #ff2d78, 0 0 20px rgba(255, 45, 120, 0.3)',
        'glow-cyan': '0 0 10px #00f0ff, 0 0 40px rgba(0, 240, 255, 0.15)',
        'glow-magenta': '0 0 10px #ff00e5, 0 0 40px rgba(255, 0, 229, 0.15)',
      },
      animation: {
        'pulse-glow': 'pulse-glow 2s ease-in-out infinite',
        'scan-line': 'scan-line 4s linear infinite',
        'fade-in': 'fade-in 0.4s ease-out forwards',
        'slide-up': 'slide-up 0.4s ease-out forwards',
        'glitch': 'glitch 3s infinite',
        'data-stream': 'data-stream 20s linear infinite',
      },
      keyframes: {
        'pulse-glow': {
          '0%, 100%': { opacity: '1' },
          '50%': { opacity: '0.6' },
        },
        'scan-line': {
          '0%': { transform: 'translateY(-100%)' },
          '100%': { transform: 'translateY(100vh)' },
        },
        'fade-in': {
          '0%': { opacity: '0' },
          '100%': { opacity: '1' },
        },
        'slide-up': {
          '0%': { opacity: '0', transform: 'translateY(12px)' },
          '100%': { opacity: '1', transform: 'translateY(0)' },
        },
        'glitch': {
          '0%, 90%, 100%': { transform: 'translate(0)' },
          '92%': { transform: 'translate(-2px, 1px)' },
          '94%': { transform: 'translate(2px, -1px)' },
          '96%': { transform: 'translate(-1px, -1px)' },
          '98%': { transform: 'translate(1px, 2px)' },
        },
        'data-stream': {
          '0%': { backgroundPosition: '0 0' },
          '100%': { backgroundPosition: '0 1000px' },
        },
      },
    },
  },
  plugins: [],
};
