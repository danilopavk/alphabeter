// The play screen's progress board: D toggles it, Esc closes it, and so does
// tapping the score (phones have no keyboard). Whether it is open is a class on
// <body>, which htmx never swaps, so the board stays open while answers refresh it.
(() => {
  const setOpen = (open) => document.body.classList.toggle("board-open", open);
  const isOpen = () => document.body.classList.contains("board-open");

  document.addEventListener("keydown", (e) => {
    if (e.ctrlKey || e.metaKey || e.altKey || e.repeat) return;
    if (e.key === "d" || e.key === "D" || e.code === "KeyD") setOpen(!isOpen());
    else if (e.key === "Escape") setOpen(false);
  });
  document.addEventListener("click", (e) => {
    if (e.target.closest("[data-board-toggle]")) setOpen(!isOpen());
  });
})();

// A right answer's letter flies off the screen (app.css), each time at least
// 60° away from the last one's way. Set here as the CSP allows no inline styles.
(() => {
  let angle = Math.random() * 360;

  document.addEventListener("htmx:afterSwap", () => {
    const fly = document.querySelector(".fly");
    if (!fly) return;
    angle = (angle + 60 + Math.random() * 240) % 360;
    const rad = (angle * Math.PI) / 180;
    fly.style.setProperty("--x", `${Math.cos(rad) * 150}vmax`);
    fly.style.setProperty("--y", `${Math.sin(rad) * 150}vmax`);
  });
})();
