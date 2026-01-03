/** @type {import('tailwindcss').Config} */
module.exports = {
  content: ["./**/*.html", "./**/*.templ", "./**/*.go"],
  theme: {
    extend: {},
  },
  plugins: [require("@iconify/tailwind").addDynamicIconSelectors()],
};
