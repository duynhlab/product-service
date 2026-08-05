package domain

type Product struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	Price         float64 `json:"price"`
	Description   string  `json:"description"`
	Category      string  `json:"category"`
	// StockQuantity is FROZEN — last written at the RFC-0021 W7 write cutover, and
	// nothing in this service has written it since. It is json:"-" so no HTTP
	// response carries a number that cannot change; it stays on the struct only
	// because product.v1/GetProducts still reports available_qty from it for
	// checkout's product-source fallback. When that fallback goes, so does this.
	StockQuantity int     `json:"-"`
}

type CreateProductRequest struct {
	Name        string  `json:"name" binding:"required"`
	Price       float64 `json:"price" binding:"required,min=0"`
	Description string  `json:"description"`
	Category    string  `json:"category"`
}
