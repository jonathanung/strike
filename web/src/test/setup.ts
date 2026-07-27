import "@testing-library/jest-dom/vitest";

HTMLDialogElement.prototype.showModal ||= function () { this.setAttribute("open", ""); };
