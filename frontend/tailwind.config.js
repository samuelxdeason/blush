/** @type {import('tailwindcss').Config} */
export default {
  content: ["./index.html", "./src/**/*.{ts,tsx}"],
  theme: {
    extend: {
      colors: {
        // Trove: archival charcoal, warm vellum and a changeable mineral accent.
        ink: "#111411",
        panel: "#191D19",
        panel2: "#222822",
        edge: "#343C34",
        muted: "#9CA49A",
        fg: "#F3EFE4",
        noir: "#090B09",
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
