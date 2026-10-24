// Прогрессивное улучшение интерфейса. Без этого файла всё работает: ссылка подтверждения ведёт
// на отдельную страницу с формой, сводка ошибок видна сверху формы. Встроенных скриптов нет (CSP, ADR-0018).
(function () {
  "use strict";

  // Подтверждение опасного действия: ссылка с data-dialog-open открывает <dialog> вместо перехода.
  document.addEventListener("click", function (event) {
    var trigger = event.target.closest("[data-dialog-open]");
    if (!trigger) {
      return;
    }
    var dialog = document.getElementById(trigger.getAttribute("data-dialog-open"));
    if (!dialog || typeof dialog.showModal !== "function") {
      return; // старый браузер: остаётся обычный переход на страницу подтверждения
    }
    event.preventDefault();
    dialog.showModal();
  });

  // Сводка ошибок формы получает фокус, чтобы экранный диктор сразу её прочитал.
  function focusErrorSummary(root) {
    var summary = root.querySelector(".error-summary");
    if (summary) {
      summary.focus();
    }
  }

  document.addEventListener("DOMContentLoaded", function () {
    focusErrorSummary(document);
  });

  // После частичного обновления htmx — то же для подменённого фрагмента.
  document.addEventListener("htmx:afterSwap", function (event) {
    if (event.target instanceof Element) {
      focusErrorSummary(event.target);
    }
  });
})();
