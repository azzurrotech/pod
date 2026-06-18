var fieldCount = 1;

document.getElementById("add-field").addEventListener("click", function() {
	var container = document.getElementById("fields-container");
	var row = document.createElement("div");
	row.className = "field-row";
	row.innerHTML = '<input type="text" name="_field_name_' + fieldCount + '" placeholder="Field name" required>' +
		'<select name="_field_type_' + fieldCount + '">' +
		'<option value="text">text</option><option value="email">email</option><option value="number">number</option>' +
		'<option value="password">password</option><option value="url">url</option><option value="tel">tel</option>' +
		'<option value="date">date</option><option value="textarea">textarea</option></select>' +
		'<button type="button" class="remove-field" onclick="this.parentElement.remove()">x</button>';
	container.appendChild(row);
	fieldCount++;
});

document.getElementById("form-builder").addEventListener("submit", async function(e) {
	e.preventDefault();
	var formData = new FormData(this);
	var name = formData.get("_form_name");
	var fields = [];
	for (var pair of formData.entries()) {
		if (pair[0].startsWith("_field_name_")) {
			var idx = pair[0].replace("_field_name_", "");
			var fname = pair[1];
			var ftype = formData.get("_field_type_" + idx) || "text";
			if (fname) fields.push({ name: fname, type: ftype, label: fname, required: true });
		}
	}
	if (fields.length === 0) { alert("Add at least one field"); return; }
	var payload = {
		id: "form_" + Date.now(),
		name: name,
		action: "/submit",
		method: "POST",
		fields: fields,
		created_at: new Date().toISOString()
	};
	var res = await fetch("/api/forms", {
		method: "POST",
		headers: { "Content-Type": "application/json" },
		body: JSON.stringify(payload)
	});
	if (res.ok) { window.location.reload(); }
	else { alert("Failed to create form"); }
});
