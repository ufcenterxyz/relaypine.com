import { heroui } from "@heroui/react";
export default heroui({
  themes: {
    light: {
      colors: {
        primary: {
          DEFAULT: "#176047",
          foreground: "#ffffff",
          50: "#eff8f3",
          100: "#dceee4",
          200: "#bddcca",
          300: "#90c3a9",
          400: "#5e9f80",
          500: "#387e5f",
          600: "#176047",
          700: "#144b39",
          800: "#123d30",
          900: "#0d3026",
        },
      },
    },
    dark: {
      colors: {
        background: "#111614",
        content1: "#161d19",
        primary: {
          DEFAULT: "#8dd7b2",
          foreground: "#10271c",
          50: "#10271c",
          100: "#183c2b",
          200: "#24583f",
          300: "#347957",
          400: "#539e77",
          500: "#73bf96",
          600: "#8dd7b2",
          700: "#ace6c7",
          800: "#d0f1df",
          900: "#ecfaf2",
        },
      },
    },
  },
  layout: { radius: { small: "6px", medium: "10px", large: "14px" } },
});
