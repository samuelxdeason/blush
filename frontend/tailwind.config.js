/** @type {import('tailwindcss').Config} */
export default {
  content: ["./index.html", "./src/**/*.{ts,tsx}"],
  theme: {
    extend: {
      colors: {
        // Dark yogurt v2 — cool plum-charcoal base (no coffee tint) so the
        // cream text and fruit accents pop hard.
        ink: "#131118", // page background (deep plum-charcoal)
        panel: "#1B1822", // cards / surfaces
        panel2: "#262230", // hover / raised surface
        edge: "#332E3F", // subtle borders
        muted: "#A79DB3", // secondary text (cool lavender-gray)
        fg: "#F8F5F1", // primary text (cream)
        noir: "#0C0B10", // immersive surfaces (feed / player chrome)
        accent: "rgb(var(--ac-rgb) / <alpha-value>)", // runtime flavor (default strawberry)
        acink: "var(--ac-ink)", // readable text on the accent
      },
      borderRadius: {
        blob: "1.375rem", // soft, spoonable card corners
      },
    },
  },
  plugins: [],
};
