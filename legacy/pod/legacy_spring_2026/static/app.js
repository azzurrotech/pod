document.addEventListener('DOMContentLoaded', () => {
    loadSchema();
});

async function loadSchema() {
    try {
        const res = await fetch('/api/schema');
        const fields = await res.json();
        renderForm(fields);
    } catch (err) {
        console.error("Error loading schema:", err);
    }
}

function renderForm(fields) {
    const container = document.getElementById('dynamic-fields');
    container.innerHTML = '';
    
    fields.forEach(field => {
        const div = document.createElement('div');
        div.innerHTML = `
            <label>${capitalize(field)}</label>
            <input type="text" name="${field}" required>
        `;
        container.appendChild(div);
    });
}

document.getElementById('record-form').addEventListener('submit', async (e) => {
    e.preventDefault();
    const formData = new FormData(e.target);
    const data = {};
    formData.forEach((value, key) => {
        data[key] = value;
    });

    try {
        const res = await fetch('/api/record', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(data)
        });
        
        if (res.ok) {
            alert('Record saved successfully!');
            e.target.reset();
            loadSchema(); // Refresh schema in case new fields were added
        } else {
            alert('Error saving record');
        }
    } catch (err) {
        console.error(err);
        alert('Network error');
    }
});

async function searchRecords() {
    const query = document.getElementById('search-input').value;
    if (!query) return;

    try {
        const res = await fetch(`/api/search?q=${encodeURIComponent(query)}`);
        const records = await res.json();
        displayResults(records);
    } catch (err) {
        console.error(err);
    }
}

function displayResults(records) {
    const area = document.getElementById('results-area');
    area.innerHTML = '';

    if (records.length === 0) {
        area.innerHTML = '<p>No records found.</p>';
        return;
    }

    records.forEach(rec => {
        const div = document.createElement('div');
        div.className = 'record-item';
        
        let inputsHtml = '';
        for (const [key, value] of Object.entries(rec.data)) {
            inputsHtml += `
                <label>${capitalize(key)}</label>
                <input type="text" value="$${value}" onchange="updateRecord('$${rec.id}', '${key}', this.value)">
            `;
        }
        
        div.innerHTML = `<strong>ID: $${rec.id}</strong><br>$${inputsHtml}`;
        area.appendChild(div);
    });
}

async function updateRecord(id, key, value) {
    // Fetch full record to preserve other fields
    const res = await fetch(`/api/search?q=${id}`); // Simple hack to get record by ID via search
    const records = await res.json();
    const record = records.find(r => r.id === id);
    
    if (record) {
        record.data[key] = value;
        await fetch('/api/record?id=' + id, {
            method: 'PUT',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(record.data)
        });
    }
}

function capitalize(str) {
    return str.charAt(0).toUpperCase() + str.slice(1);
}