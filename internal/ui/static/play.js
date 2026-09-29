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
