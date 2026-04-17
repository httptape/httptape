import { useCallback, useEffect, useState } from "react";
import { getProducts, getProfile, createCart, type Product, type UserProfile, type DataSource } from "./api";
import { useHealthStream } from "./useHealthStream";
import { ProfileCard } from "./components/ProfileCard";
import { Assistant } from "./components/Assistant";
import { ArchitectureDiagram } from "./components/ArchitectureDiagram";
import { Instructions } from "./components/Instructions";
import "./App.css";

const SOURCE_CONFIG: Record<DataSource, { label: string; className: string; description: string }> = {
  upstream:   { label: "Live",     className: "source-live",  description: "Serving from live upstream backend" },
  "l1-cache": { label: "L1 Cache", className: "source-l1",    description: "In-memory cache (raw, current session)" },
  "l2-cache": { label: "L2 Cache", className: "source-l2",    description: "Disk cache (redacted, persisted)" },
};

function App() {
  const [products, setProducts] = useState<Product[]>([]);
  const [profile, setProfile] = useState<UserProfile | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [cartMessage, setCartMessage] = useState<string | null>(null);
  const source = useHealthStream();

  const fetchData = useCallback(() => {
    setLoading(true);
    setError(null);
    Promise.all([getProducts(), getProfile()])
      .then(([productsRes, profileRes]) => {
        setProducts(productsRes.data);
        setProfile(profileRes.data);
      })
      .catch((err) => setError(err.message))
      .finally(() => setLoading(false));
  }, []);

  useEffect(() => {
    fetchData();
  }, [fetchData, source]);

  const handleAddToCart = async (product: Product) => {
    try {
      const res = await createCart();
      setCartMessage(`Added "${product.name}" — cart ${res.data.cart_id}`);
      setTimeout(() => setCartMessage(null), 3000);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to create cart");
    }
  };

  const sourceInfo = source ? SOURCE_CONFIG[source] : null;

  return (
    <div className="app">
      {/* Tech panel — left sidebar */}
      <aside className="tech-panel">
        <img src="/logo.png" alt="httptape" className="logo" />

        <div className="status-row">
          {sourceInfo && (
            <span className={`source-badge ${sourceInfo.className}`}>
              <span className="dot" />
              {sourceInfo.label}
            </span>
          )}
          <button className="refresh-btn" onClick={fetchData} disabled={loading}>↻</button>
        </div>

        <ArchitectureDiagram source={source} />
        <Instructions />
      </aside>

      {/* Business panel — main content */}
      <main className="business-panel">
        {cartMessage && <div className="toast">{cartMessage}</div>}

        {loading && <p className="status">Loading...</p>}
        {error && <p className="status error">{error}</p>}

        {!loading && !error && (
          <>
            {profile && <ProfileCard profile={profile} />}
            <h2 className="section-title">Products</h2>
            <div className="product-grid">
              {products.map((p) => (
                <div key={p.id} className="product-card">
                  <div className="product-info">
                    <h3>{p.name}</h3>
                    <span className="price">${p.price.toFixed(2)}</span>
                  </div>
                  <p className="description">{p.description}</p>
                  <button onClick={() => handleAddToCart(p)}>Add to Cart</button>
                </div>
              ))}
            </div>
            <Assistant />
          </>
        )}
      </main>
    </div>
  );
}

export default App;
