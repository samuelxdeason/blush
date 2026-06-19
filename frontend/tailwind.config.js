/** @type {import('tailwindcss').Config} */
export default {
  content: ["./index.html", "./src/**/*.{ts,tsx}"],
  theme: {
    extend: {
      colors: {
        ink: "#0f1419",
        panel: "#161b22",
        panel2: "#1c2330",
        edge: "#2a3140",
        muted: "#8b98a5",
        accent: "#1d9bf0",
      },
    },
  },
  plugins: [],
};
