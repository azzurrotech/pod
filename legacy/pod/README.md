# POD: Persistent Object Database

**POD** is a lightweight, zero-dependency Database Management System (DBMS) built entirely with the Go standard library. It leverages the native filesystem (EXT4 compatible) to store records as JSON files while maintaining an internal index for rapid lookups.

Designed for simplicity and portability, POD requires no external database servers (like PostgreSQL or MySQL). It is ideal for small-to-medium applications, prototyping, or embedded systems where a full RDBMS is overkill.

> **Author:** Azzurro Technology Inc
> **Website:** [https://azzurro.tech](https://azzurro.tech)
> **Contact:** [info@azzurro.tech](mailto:info@azzurro.tech)

---

## ✨ Features

- **Zero Dependencies:** Built exclusively with Go standard library, HTML, CSS, and JS.
- **Filesystem-Based Storage:** Data is stored as individual JSON files in a directory structure.
- **Internal Indexing:** Maintains a lightweight `index.json` for O(1) lookups without scanning the entire filesystem.
- **Dynamic Form Builder:** Web UI allows users to define schemas and create records on the fly.
- **Full CRUD Support:** Create, Read, Update, and Delete records via UI or API.
- **Search Capability:** Full-text search across all record fields.
- **RESTful API:** Programmatic access for integration with other tools.
- **Containerized:** Ready-to-run with Docker and Docker Compose.

---

## 🚀 Deployment Options

### Option 1: Docker Compose (Recommended)
Ideal for production environments requiring easy scaling and data persistence.

1. Ensure `docker-compose.yml` is in your project root.
2. Run: `docker-compose up -d --build`
3. Data persists in the `pod-data` volume.
4. Stop with: `docker-compose down`

### Option 2: Standalone Binary
Best for embedded devices or minimal footprint environments.

1. Build: `CGO_ENABLED=0 go build -o pod-server main.go`
2. Run: `./pod-server`
3. Ensure the `storage` directory has write permissions.

### Option 3: Kubernetes
Deploy as a stateful set for high availability.

1. Create a PersistentVolumeClaim for `/app/storage`.
2. Deploy the container image with the volume mounted.
3. Expose via a LoadBalancer or Ingress controller.

### Option 4: Local Development
Quick setup for testing and prototyping.

1. Install Go 1.22+.
2. Run: `go run main.go`
3. Access `http://localhost:8080`.

---

## 📖 Usage Guide

### Creating Records
Navigate to the "Form Builder" section in the web UI. The form automatically detects existing fields or defaults to `name`, `email`, and `status`. Enter your data and click "Save Record". The system instantly creates a JSON file in the data directory and updates the internal index.

### Searching Records
Use the search bar to query any field value. The system performs a case-insensitive substring match across all records. Results are displayed in a list sorted by the last updated timestamp.

### Editing Records
Click on any field in the search results to edit it directly. Changes are saved immediately upon losing focus (blur event) or by triggering the update API. No explicit "Save" button is required for inline edits.

### API Integration
Interact programmatically via the REST API.
- `GET /api/schema`: Returns the current list of active fields.
- `POST /api/record`: Creates a new record (JSON body required).
- `PUT /api/record?id=<ID>`: Updates an existing record.
- `GET /api/search?q=<term>`: Returns matching records.

---

## 📄 License

This project is licensed under the **MIT License**. See the [LICENSE](LICENSE) file for details.

<div align="center">

**POD** is a project by **Azzurro Technology Inc** © 2026

</div>