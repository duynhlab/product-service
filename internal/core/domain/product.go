package domain

type Product struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Price       float64 `json:"price"`
	Description string  `json:"description"`
	Category    string  `json:"category"`
	// Status and Version are omitted from public payloads: the public catalog only
	// ever contains ACTIVE rows, so the field would be a constant there, and the
	// concurrency token is an operator concern (RFC-0023 slice B). The protected
	// handlers marshal their own view that includes both.
	Status  ProductStatus `json:"-"`
	Version int64         `json:"-"`
}

type CreateProductRequest struct {
	Name        string  `json:"name" binding:"required"`
	Price       float64 `json:"price" binding:"required,min=0"`
	Description string  `json:"description"`
	Category    string  `json:"category"`
}
