/** @type {import('tailwindcss').Config} */
module.exports = {
  content: [
    "./cmd/**/*.{html,templ,go}",
    "./internal/**/*.{html,templ,go}",
    "!./**/*_test.go",
  ],
  theme: {
    extend: {},
  },
  plugins: [
    require("@iconify/tailwind").addDynamicIconSelectors(),
    require("daisyui"),
  ],
  daisyui: {
    themes: true, // Enable all DaisyUI themes to support any configured theme
  },
};
