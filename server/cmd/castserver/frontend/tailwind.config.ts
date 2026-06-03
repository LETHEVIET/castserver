import type { Config } from 'tailwindcss';

export default {
  content: [
    './src/**/*.{html,js,svelte,ts}',
    './node_modules/flowbite-svelte/**/*.{html,js,svelte,ts}',
    './node_modules/flowbite/**/*.js',
  ],
  darkMode: 'class',
  theme: {
    extend: {
      colors: {
        primary: {
          50: '#ecfdf5', 100: '#d1fae5', 200: '#a7f3d0', 300: '#6ee7b7',
          400: '#34d399', 500: '#10b981', 600: '#059669', 700: '#047857',
          800: '#065f46', 900: '#064e3b',
        },
        zinc: {
          150: '#ececed', 250: '#dcdcdf', 350: '#bbbbc1',
          450: '#898992', 455: '#82828b', 555: '#62626a',
          650: '#484851', 850: '#1f1f22', 1000: '#050506',
        },
      },
    },
  },
  plugins: [require('flowbite/plugin')],
} satisfies Config;
