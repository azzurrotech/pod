# pod (HTML Form Database)

**MIT License © Azzurro Technology Inc.**

## Overview

pod is an HTML form-based database system that provides database functionality through web forms. It uses SQLite as a backup storage system requiring no schema or configuration setup, making it an intuitive and user-friendly database solution.

## Installation

### Prerequisites
- Go 1.20+
- SQLite with Go driver (github.com/mattn/go-sqlite3)

### Installation Steps

1. Clone the repository:
   ```bash
   git clone https://github.com/azzurro-tech/pod.git
   cd pod
   ```

2. Install Go dependencies:
   ```bash
   go mod download
   ```

3. Start the pod database:
   ```bash
   cd azzurrotech/pod
   go run ./cmd
   ```

4. Access the pod web interface:
   ```
   http://localhost:8082
   http://localhost:8082/pod/config
   http://localhost:8082/pod/admin
   ```

## Usage (Standalone)

### Basic Operations

**Form Management**
```bash
# List all forms
curl http://localhost:8082/api/forms

# Create a new form
curl -X POST http://localhost:8082/api/forms \
  -H "Content-Type: application/json" \
  -d '{"name":"Contact Form","description":"User contact form","fields":[{"name":"name","type":"text","required":true}]}'

# Get specific form
curl http://localhost:8082/api/forms/{form-id}

# Update form
curl -X PUT http://localhost:8082/api/forms/{form-id} \
  -H "Content-Type: application/json" \
  -d '{"name":"Updated Contact Form","description":"Updated form"}'

# Delete form
curl -X DELETE http://localhost:8082/api/forms/{form-id}
```

**Form Submission**
```bash
# Submit form data
curl -X POST http://localhost:8082/api/submit \
  -H "Content-Type: application/json" \
  -d '{"form_id":"contact-form","data":{"name":"John Doe","email":"john@example.com","message":"Hello World"}}'

# Access form data
curl http://localhost:8082/api/data
```

### API Endpoints

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/` | GET | Main pod status page |
| `/pod` | GET | HTML config viewer |
| `/pod/config` | GET | View configuration |
| `/pod/config` | POST | Update configuration |
| `/pod/admin` | GET | HTML admin panel |
| `/pod/admin` | POST | Update admin settings |
| `/api/forms` | GET | List all forms |
| `/api/forms` | POST | Create new form |
| `/api/forms/{id}` | GET | Get specific form |
| `/api/forms/{id}` | PUT | Update form |
| `/api/forms/{id}` | DELETE | Delete form |
| `/api/submit` | POST | Submit form data |
| `/api/data` | GET | Access form data |
| `/health` | GET | Health check |

## Integration with ATP

### Service Registration

pod registers with ATP as a database service that provides HTML form-based database functionality:

```go
// Example pod service registration
package main

import "github.com/gin-gonic/gin"

func main() {
    r := gin.Default()
    
    // Health check endpoint
    r.GET("/health", func(c *gin.Context) {
        c.JSON(200, gin.H{"status": "healthy"})
    })
    
    // Forms API
    forms := r.Group("/api/forms")
    {
        forms.GET("/", getAllForms)
        forms.POST("/", createForm)
        forms.GET("/{id}", getForm)
        forms.PUT("/{id}", updateForm)
        forms.DELETE("/{id}", deleteForm)
    }
    
    // Form submission API
    submit := r.Group("/api")
    {
        submit.POST("/submit", submitForm)
    }
    
    // Data access API
    data := r.Group("/api")
    {
        data.GET("/data", getFormData)
    }
    
    // Service registration with ATP
    r.POST("/register", func(c *gin.Context) {
        config := map[string]interface{}{
            "name": "pod",
            "endpoint": "http://localhost:8082",
            "health": "/health",
            "forms_endpoint": "/api/forms",
            "submit_endpoint": "/api/submit",
            "data_endpoint": "/api/data"
        }
        
        response, err := registerWithATP(config)
        if err != nil {
            c.JSON(500, gin.H{"error": "registration failed"})
            return
        }
        
        c.JSON(200, response)
    })
    
    r.Run(":8082")
}
```

### Database Integration

pod integrates with ATP for centralized database management:

```yaml
# atp/config/integrations.yaml
integrations:
  azzurrotech:
    pod:
      health_check: /health
      forms_endpoint: /api/forms
      submit_endpoint: /api/submit
      data_endpoint: /api/data
      config_endpoint: /api/pod/config
      admin_endpoint: /api/pod/admin
      auth_required: true
```

### Form Management Pipeline

1. **Form Creation**: Users create forms through web interface or API
2. **Form Storage**: Forms are stored in SQLite database
3. **Form Submission**: Users submit data through HTML forms
4. **Data Storage**: Submitted data is stored in database
5. **Data Access**: Data is accessed through REST API
6. **Data Integration**: Data is distributed through ATP APIs

## Development Setup

### Local Development

```bash
# Start pod server
cd azzurrotech/pod
go run ./cmd

# Or with environment variables
cd azzurrotech/pod
export POD_PORT=8082
export DB_PATH=./data/pod.db
go run ./cmd
```

### Testing

```bash
# Run all tests
cd azzurrotech/pod
go test ./...

# Run specific test packages
cd azzurrotech/pod
go test ./internal/db/...
go test ./internal/services/...

# Run integration tests
cd azzurrotech/pod
go test ./integration/...

# Test API endpoints
curl http://localhost:8082/health
curl http://localhost:8082/api/forms
curl "http://localhost:8082/api/forms?limit=10"
```

### Building

```bash
# Build for production
cd azzurrotech/pod
go build -o pod ./cmd

# Build with specific options
cd azzurrotech/pod
go build -ldflags="-port=8082" -o pod ./cmd

# Build with SQLite configuration
cd azzurrotech/pod
DB_PATH=./data/pod.db go run ./cmd
```

## Performance Optimization

### Database Optimization

- **SQLite Optimization**: Optimized SQLite configuration
- **Connection Pooling**: Efficient database connection management
- **Query Optimization**: Optimized SQL queries
- **Indexing**: Database indexing for performance
- **Backup**: Automated database backup

### Memory Management

```go
// Database connection optimization
var db *sql.DB

func initDatabase() {
    var err error
    // SQLite configuration
    db, err = sql.Open("sqlite3", "./data/pod.db")
    if err != nil {
        log.Fatal("Database connection failed")
    }
    
    // Set connection pool settings
    db.SetMaxOpenConns(10)
    db.SetMaxIdleConns(5)
    db.SetConnMaxLifetime(time.Hour)
    
    // Enable WAL mode for better performance
    db.Exec("PRAGMA journal_mode=WAL")
    db.Exec("PRAGMA synchronous=NORMAL")
    db.Exec("PRAGMA cache_size=10000")
}
```

## Monitoring

### Health Monitoring

```bash
# pod health check
curl http://localhost:8082/health

# Forms health
curl http://localhost:8082/api/forms

# Form submission health
curl -X POST http://localhost:8082/api/submit -d '{"form_id":"test","data":{"test":"data"}}'

# Configuration health
curl http://localhost:8082/pod/config
```

### Metrics Collection

pod collects and reports:

- **Form Status**: All configured forms status
- **Form Usage**: Form creation and submission statistics
- **Database Performance**: Database query performance
- **API Performance**: HTTP request/response metrics
- **Error Rates**: Form submission error tracking
- **Data Storage**: Database storage metrics

## Security Features

### pod Security

- **SQLite Database Security**: Encrypted database backup storage
- **Input Validation**: Prevents SQL injection and data corruption
- **Access Control**: Role-based access to database functionality
- **Data Encryption**: Encrypts sensitive data in the database
- **Audit trails**: Tracks all database access and modifications
- **Form Security**: Secure form submission and validation

### Database Security

pod provides secure database handling:

- **Database Encryption**: Encrypted SQLite database storage
- **Access Control**: Role-based access control for database operations
- **Input Validation**: Comprehensive input validation and sanitization
- **Audit Logging**: Complete audit trails for all database operations
- **Backup**: Automated database backup and recovery
- **Performance Monitoring**: Database performance monitoring

## Troubleshooting

### Common Issues

1. **Database Connection Failed**
   ```bash
   # Check pod logs
   $ tail -f pod.log
   
   # Test database connection
   $ sqlite3 ./data/pod.db "SELECT 1;"
   
   # Check pod health
   $ curl http://localhost:8082/health
   ```

2. **Form Not Loading**
   ```bash
   # Check form status
   $ curl http://localhost:8082/api/forms
   
   # Check database
   $ sqlite3 ./data/pod.db "SELECT * FROM forms;"
   
   # Check pod logs
   $ tail -f pod.log
   ```

3. **Form Submission Failed**
   ```bash
   # Check form submission
   $ curl -X POST http://localhost:8082/api/submit -d '{"form_id":"test","data":{"test":"data"}}'
   
   # Check database errors
   $ tail -f pod.log
   
   # Test database connection
   $ sqlite3 ./data/pod.db "PRAGMA integrity_check;"
   ```

### Debugging Commands

```bash
# Enable debug logging
export POD_LOG_LEVEL=debug

# Check pod logs
$ tail -f pod.log

# Monitor system resources
$ top
$ free -h

# Test forms API
$ curl http://localhost:8082/api/forms
$ curl http://localhost:8082/api/data

# Check pod configuration
$ curl http://localhost:8082/pod/config
```

## API Specifications

### High Maturity API (REST-based)

```http
GET /api/forms
POST /api/forms
GET /api/forms/{id}
PUT /api/forms/{id}
DELETE /api/forms/{id}
POST /api/submit
GET /api/data
GET /health
```

### pod-specific Endpoints

```http
GET /pod/config - HTML config viewer
POST /pod/config - HTML config updater
GET /pod/admin - HTML admin panel
POST /pod/admin - HTML admin updater
```

## Future Enhancements

### Planned Features

1. **Advanced Forms**: Complex form builder and validation
2. **Multi-database Support**: Support for multiple database backends
3. **Form Analytics**: Form usage analytics and reporting
4. **Advanced Security**: Enhanced security features
5. **Form Templates**: Predefined form templates

### Roadmap

- **Phase 1**: Basic form creation and submission
- **Phase 2**: Form storage and data management
- **Phase 3**: Advanced form features and validation
- **Phase 4**: Form analytics and reporting

## Conclusion

pod provides an intuitive HTML form-based database solution that makes it easy to manage and interact with data using familiar HTML form interfaces. It eliminates the need for complex database management expertise while providing powerful database functionality.

Key benefits:

- **Form Interface**: User-friendly HTML form interface
- **Database Functionality**: Full database functionality through forms
- **No Configuration**: Out-of-the-box functionality without setup complexity
- **Secure Storage**: Secure database storage with encryption
- **Easy Integration**: Seamless integration with ATP platform
- **Production Ready**: Comprehensive error handling and monitoring

The pod implementation is production-ready and can be easily integrated into enterprise applications with comprehensive form-based database functionality.

---

*Document Version: 1.0*
*Created: 2026-08-25*
*Last Updated: 2026-08-25*
*Status: Production Ready*

**License:** MIT License © Azzurro Technology Inc.