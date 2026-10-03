/**
 * @license MIT
 * EcommerceStorefront Component — Morphic Design System
 * 
 * Photography Assets: Unsplash Open License (Royalty-free commercial use)
 * Credits: Paul Esch-Laurent, Christian Wiediger, Moritz Kindler, Daniel Korpai, Dmitry Chernyshov, Luke Chesser
 */

import React, { useState } from 'react';
import { motion } from 'motion/react';
import { Icon } from '../components/communication/Icon.js';
import { Button } from '../components/actions/Button.js';
import { Chip } from '../components/selection/index.js';

export interface ProductItem {
  id: string;
  name: string;
  category: string;
  price: string;
  rating: number;
  salesCount: string;
  inStock: boolean;
  imageUrl: string;
}

export const EcommerceStorefront: React.FC<{
  onAddToCart?: (p: ProductItem) => void;
}> = ({ onAddToCart }) => {
  const [selectedCategory, setSelectedCategory] = useState('All');

  const products: ProductItem[] = [
    {
      id: '1',
      name: 'Aurora Wireless Noise Cancelling Headphone Pro',
      category: 'Audio',
      price: '$189.00',
      rating: 4.9,
      salesCount: '1.2k Sold',
      inStock: true,
      imageUrl: 'https://images.unsplash.com/photo-1505740420928-5e560c06d30e?w=800&auto=format&fit=crop&q=80',
    },
    {
      id: '2',
      name: 'Ultra-Slim Mechanical Wireless Keyboard 75% RGB',
      category: 'Accessories',
      price: '$89.00',
      rating: 4.8,
      salesCount: '850 Sold',
      inStock: true,
      imageUrl: 'https://images.unsplash.com/photo-1587829741301-dc798b83add3?w=800&auto=format&fit=crop&q=80',
    },
    {
      id: '3',
      name: 'Ergonomic Precision Optical Mouse with Magnetic Scroll',
      category: 'Accessories',
      price: '$49.00',
      rating: 4.7,
      salesCount: '2.4k Sold',
      inStock: true,
      imageUrl: 'https://images.unsplash.com/photo-1527864550417-7fd91fc51a46?w=800&auto=format&fit=crop&q=80',
    },
    {
      id: '4',
      name: 'Smart Fitness Tracker with AMOLED Display & Dual GPS',
      category: 'Wearable',
      price: '$129.00',
      rating: 4.9,
      salesCount: '940 Sold',
      inStock: true,
      imageUrl: 'https://images.unsplash.com/photo-1523275335684-37898b6baf30?w=800&auto=format&fit=crop&q=80',
    },
    {
      id: '5',
      name: 'Studio Micro-Dynamic USB Condenser Microphone Podcaster',
      category: 'Audio',
      price: '$99.00',
      rating: 4.8,
      salesCount: '620 Sold',
      inStock: false,
      imageUrl: 'https://images.unsplash.com/photo-1590602847861-f357a9332bbc?w=800&auto=format&fit=crop&q=80',
    },
    {
      id: '6',
      name: 'Portable Fast Charger GaN 65W Triple Port Type-C Power Delivery',
      category: 'Power',
      price: '$29.00',
      rating: 4.9,
      salesCount: '3.1k Sold',
      inStock: true,
      imageUrl: 'https://images.unsplash.com/photo-1583863788434-e58a36330cf0?w=800&auto=format&fit=crop&q=80',
    },
  ];

  const filteredProducts =
    selectedCategory === 'All'
      ? products
      : products.filter((p) => p.category.toLowerCase() === selectedCategory.toLowerCase());

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: '28px' }}>
      {/* Header & Filter Controls */}
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', flexWrap: 'wrap', gap: '16px' }}>
        <div>
          <h1 style={{ margin: '0 0 4px', fontSize: '26px', fontWeight: 700 }}>Product Catalog & Storefront</h1>
          <p style={{ margin: 0, color: 'var(--md-sys-color-on-surface-variant)', fontSize: '13px' }}>
            Implementation example of the Global Design System for digital e-commerce & retail.
          </p>
        </div>

        {/* Category Pills */}
        <div style={{ display: 'flex', gap: '8px' }}>
          {['All', 'Audio', 'Accessories', 'Wearable', 'Power'].map((cat) => (
            <Chip
              key={cat}
              variant="filter"
              selected={selectedCategory === cat}
              onClick={() => setSelectedCategory(cat)}
            >
              {cat}
            </Chip>
          ))}
        </div>
      </div>

      {/* Product Cards Grid */}
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(3, 1fr)', gap: '20px' }}>
        {filteredProducts.map((p) => (
          <motion.div
            key={p.id}
            whileHover={{ y: -4, transition: { duration: 0.18 } }}
            style={{
              borderRadius: 'var(--radius-xl)',
              backgroundColor: 'var(--md-sys-color-surface)',
              border: '1px solid var(--md-sys-color-border)',
              padding: '20px',
              display: 'flex',
              flexDirection: 'column',
              justifyContent: 'space-between',
              boxShadow: 'var(--md-sys-elevation-level1)',
            }}
          >
            <div>
              {/* Product Visual Area with Real High-Res Image */}
              <div
                style={{
                  height: '180px',
                  borderRadius: 'var(--radius-lg)',
                  backgroundColor: 'var(--md-sys-color-surface-container)',
                  overflow: 'hidden',
                  position: 'relative',
                  marginBottom: '16px',
                }}
              >
                <img
                  src={p.imageUrl}
                  alt={p.name}
                  style={{
                    width: '100%',
                    height: '100%',
                    objectFit: 'cover',
                    transition: 'transform 0.3s ease',
                  }}
                />
                <span
                  style={{
                    position: 'absolute',
                    top: '10px',
                    right: '10px',
                    padding: '4px 8px',
                    borderRadius: 'var(--radius-pill)',
                    backgroundColor: 'rgba(255, 255, 255, 0.9)',
                    color: 'var(--md-sys-color-on-surface)',
                    fontSize: '11px',
                    fontWeight: 600,
                    display: 'flex',
                    alignItems: 'center',
                    gap: '4px',
                    boxShadow: 'var(--md-sys-elevation-level1)',
                  }}
                >
                  <Icon name="star" size={13} color="var(--md-sys-color-warning)" filled />
                  <span>{p.rating}</span>
                </span>
              </div>

              {/* Category & Title */}
              <div style={{ fontSize: '11px', color: 'var(--md-sys-color-primary)', fontWeight: 600, textTransform: 'uppercase', letterSpacing: '0.04em' }}>
                {p.category}
              </div>
              <h3 style={{ margin: '4px 0 8px', fontSize: '14px', fontWeight: 600, lineHeight: 1.35 }}>
                {p.name}
              </h3>
              <div style={{ fontSize: '12px', color: 'var(--md-sys-color-on-surface-variant)' }}>
                {p.salesCount} • {p.inStock ? <span style={{ color: 'var(--md-sys-color-primary)', fontWeight: 600 }}>In Stock</span> : <span style={{ color: 'var(--md-sys-color-error)', fontWeight: 600 }}>Out of Stock</span>}
              </div>
            </div>

            {/* Price & Add to Cart Button */}
            <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginTop: '20px', paddingTop: '16px', borderTop: '1px solid var(--md-sys-color-border)' }}>
              <div>
                <div style={{ fontSize: '11px', color: 'var(--md-sys-color-on-surface-variant)', fontWeight: 500 }}>Price</div>
                <div style={{ fontSize: '17px', fontWeight: 700, color: 'var(--md-sys-color-on-surface)', fontFeatureSettings: '"tnum" 1' }}>{p.price}</div>
              </div>
              <Button
                variant="filled"
                size="sm"
                icon={<Icon name="shopping_bag" size={16} />}
                disabled={!p.inStock}
                onClick={() => onAddToCart && onAddToCart(p)}
              >
                + Cart
              </Button>
            </div>
          </motion.div>
        ))}
      </div>
    </div>
  );
};
