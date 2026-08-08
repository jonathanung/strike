import "@testing-library/jest-dom/vitest";

HTMLDialogElement.prototype.showModal ||= function () {
  this.setAttribute("open", "");
  this.open = true;
};
HTMLDialogElement.prototype.close ||= function () {
  this.removeAttribute("open");
  this.open = false;
  this.dispatchEvent(new Event("close"));
};
