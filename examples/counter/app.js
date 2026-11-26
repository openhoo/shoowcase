const count = document.getElementById("count");
let total = 0;
document.getElementById("add").addEventListener("click", () => {
  count.textContent = String(++total);
});
document.getElementById("reset").addEventListener("click", () => {
  total = 0;
  count.textContent = "0";
});
document.getElementById("add").disabled = false;
document.getElementById("reset").disabled = false;
