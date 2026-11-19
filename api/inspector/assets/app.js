// The inspector's page script. The server renders the first view; this script
// keeps an open run live by asking the hydration endpoint for the events after
// the last sequence on the page. A run that ends, gains a gate or changes state
// is re-rendered by reloading the page, so the server stays the only renderer.
(function () {
  "use strict";

  var island = document.getElementById("inspector-run");
  if (!island) {
    return;
  }
  var pool = null;
  try {
    pool = JSON.parse(island.textContent || "{}");
  } catch (_err) {
    return;
  }
  var run = (pool && pool.run) || {};
  if (!run.id) {
    return;
  }

  var root = document.querySelector('[data-page="run"]');
  var eventList = document.querySelector("[data-events]");

  function lastSeq() {
    var seq = 0;
    if (!eventList) {
      return seq;
    }
    var items = eventList.querySelectorAll("[data-seq]");
    if (items.length) {
      seq = Number(items[items.length - 1].getAttribute("data-seq")) || 0;
    }
    return seq;
  }

  function append(events) {
    if (!eventList) {
      if (!events.length) {
        return;
      }
      eventList = document.createElement("ol");
      eventList.className = "events";
      eventList.setAttribute("data-events", "");
      var section = document.querySelector("#events");
      if (section) {
        var empty = section.querySelector('[data-events="none"]');
        if (empty) {
          empty.remove();
        }
        section.appendChild(eventList);
      }
    }
    events.forEach(function (event) {
      var item = document.createElement("li");
      item.setAttribute("data-seq", String(event.seq));
      item.setAttribute("data-event-type", String(event.type || ""));
      var seq = document.createElement("span");
      seq.className = "seq";
      seq.textContent = String(event.seq);
      var kind = document.createElement("span");
      kind.className = "type";
      kind.textContent = String(event.type || "");
      item.appendChild(seq);
      item.appendChild(document.createTextNode(" "));
      item.appendChild(kind);
      eventList.appendChild(item);
    });
  }

  function changed(next) {
    if (!root) {
      return false;
    }
    if ((next.state || "") !== root.getAttribute("data-state")) {
      return true;
    }
    if ((next.error_code || "") !== (root.getAttribute("data-error-code") || "")) {
      return true;
    }
    var gates = next.gates || [];
    var shown = document.querySelectorAll("[data-gate]").length;
    return gates.length !== shown;
  }

  function tick() {
    var after = lastSeq();
    fetch("live?after=" + encodeURIComponent(String(after)), {
      credentials: "same-origin",
      headers: { Accept: "application/json" }
    })
      .then(function (response) {
        if (!response.ok) {
          throw new Error("live " + response.status);
        }
        return response.json();
      })
      .then(function (next) {
        if (!next || !next.run) {
          return;
        }
        if (changed(next.run)) {
          window.location.reload();
          return;
        }
        append(next.events || []);
      })
      .catch(function () {
        // A failed poll keeps the last good view; the next tick retries.
      });
  }

  window.setInterval(tick, 2000);
})();
