/** @type {import('tailwindcss').Config} */
export default {
  content: ["./index.html", "./src/**/*.{ts,tsx}"],
  theme: {
    extend: {
      colors: {
        // Trove: a clean launcher-like dark system with one vivid mood color.
        ink: "#0F0F11",
        panel: "#18181C",
        panel2: "#222228",
        edge: "#303037",
        muted: "#9B9BA4",
        fg: "#F7F7F8",
        noir: "#08080A",
        accent: "rgb(var(--ac-rgb) / <alpha-value>)",
        acink: "var(--ac-ink)", // readable text on the accent
      },
      borderRadius: {
        blob: "1rem",
      },
    },
  },
  plugins: [],
};
