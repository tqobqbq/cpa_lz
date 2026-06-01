import { useState, useMemo } from 'react';
import { useTranslation } from 'react-i18next';
import { Card } from '@/components/ui/Card';
import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Input';
import { Modal } from '@/components/ui/Modal';
import { Select } from '@/components/ui/Select';
import { MODEL_PRICE_FIELDS, type ModelPrice, type ModelPriceFieldKey } from '@/utils/usage';
import styles from '@/pages/UsagePage.module.scss';

export interface PriceSettingsCardProps {
  modelNames: string[];
  modelPrices: Record<string, ModelPrice>;
  onPricesChange: (prices: Record<string, ModelPrice>) => void;
}

export function PriceSettingsCard({
  modelNames,
  modelPrices,
  onPricesChange
}: PriceSettingsCardProps) {
  const { t } = useTranslation();
  const emptyPriceFields = () =>
    MODEL_PRICE_FIELDS.reduce(
      (acc, field) => {
        acc[field.key] = '';
        return acc;
      },
      {} as Record<ModelPriceFieldKey, string>
    );
  const priceToFields = (price?: ModelPrice) =>
    MODEL_PRICE_FIELDS.reduce(
      (acc, field) => {
        acc[field.key] = price ? String(price[field.key]) : '';
        return acc;
      },
      {} as Record<ModelPriceFieldKey, string>
    );
  const fieldsToPrice = (fields: Record<ModelPriceFieldKey, string>): ModelPrice =>
    MODEL_PRICE_FIELDS.reduce(
      (acc, field) => {
        const parsed = Number.parseFloat(fields[field.key]);
        acc[field.key] = Number.isFinite(parsed) && parsed >= 0 ? parsed : 0;
        return acc;
      },
      {} as ModelPrice
    );

  const [selectedModel, setSelectedModel] = useState('');
  const [priceFields, setPriceFields] = useState<Record<ModelPriceFieldKey, string>>(
    emptyPriceFields
  );

  const [editModel, setEditModel] = useState<string | null>(null);
  const [editFields, setEditFields] = useState<Record<ModelPriceFieldKey, string>>(
    emptyPriceFields
  );

  const updatePriceField = (key: ModelPriceFieldKey, value: string) => {
    setPriceFields((current) => ({ ...current, [key]: value }));
  };

  const updateEditField = (key: ModelPriceFieldKey, value: string) => {
    setEditFields((current) => ({ ...current, [key]: value }));
  };

  const handleSavePrice = () => {
    if (!selectedModel) return;
    const newPrices = { ...modelPrices, [selectedModel]: fieldsToPrice(priceFields) };
    onPricesChange(newPrices);
    setSelectedModel('');
    setPriceFields(emptyPriceFields());
  };

  const handleDeletePrice = (model: string) => {
    const newPrices = { ...modelPrices };
    delete newPrices[model];
    onPricesChange(newPrices);
  };

  const handleOpenEdit = (model: string) => {
    setEditModel(model);
    setEditFields(priceToFields(modelPrices[model]));
  };

  const handleSaveEdit = () => {
    if (!editModel) return;
    const newPrices = { ...modelPrices, [editModel]: fieldsToPrice(editFields) };
    onPricesChange(newPrices);
    setEditModel(null);
  };

  const handleModelSelect = (value: string) => {
    setSelectedModel(value);
    setPriceFields(priceToFields(modelPrices[value]));
  };

  const options = useMemo(
    () => [
      { value: '', label: t('usage_stats.model_price_select_placeholder') },
      ...Array.from(new Set([...Object.keys(modelPrices), ...modelNames]))
        .sort((a, b) => a.localeCompare(b))
        .map((name) => ({ value: name, label: name }))
    ],
    [modelNames, modelPrices, t]
  );

  return (
    <Card title={t('usage_stats.model_price_settings')}>
      <div className={styles.pricingSection}>
        {/* Price Form */}
        <div className={styles.priceForm}>
          <div className={styles.formRow}>
            <div className={styles.formField}>
              <label>{t('usage_stats.model_name')}</label>
              <Select
                value={selectedModel}
                options={options}
                onChange={handleModelSelect}
                placeholder={t('usage_stats.model_price_select_placeholder')}
              />
            </div>
            {MODEL_PRICE_FIELDS.map((field) => (
              <div key={field.key} className={styles.formField}>
                <label>{t(field.labelKey)} ($/1M)</label>
                <Input
                  type="number"
                  value={priceFields[field.key]}
                  onChange={(e) => updatePriceField(field.key, e.target.value)}
                  placeholder="0.00"
                  step="0.0001"
                />
              </div>
            ))}
            <Button variant="primary" onClick={handleSavePrice} disabled={!selectedModel}>
              {t('common.save')}
            </Button>
          </div>
        </div>

        {/* Saved Prices List */}
        <div className={styles.pricesList}>
          <h4 className={styles.pricesTitle}>{t('usage_stats.saved_prices')}</h4>
          {Object.keys(modelPrices).length > 0 ? (
            <div className={styles.pricesGrid}>
              {Object.entries(modelPrices).map(([model, price]) => (
                <div key={model} className={styles.priceItem}>
                  <div className={styles.priceInfo}>
                    <span className={styles.priceModel}>{model}</span>
                    <div className={styles.priceMeta}>
                      {MODEL_PRICE_FIELDS.map((field) => (
                        <span key={field.key}>
                          {t(field.labelKey)}: ${price[field.key].toFixed(4)}/1M
                        </span>
                      ))}
                    </div>
                  </div>
                  <div className={styles.priceActions}>
                    <Button variant="secondary" size="sm" onClick={() => handleOpenEdit(model)}>
                      {t('common.edit')}
                    </Button>
                    <Button variant="danger" size="sm" onClick={() => handleDeletePrice(model)}>
                      {t('common.delete')}
                    </Button>
                  </div>
                </div>
              ))}
            </div>
          ) : (
            <div className={styles.hint}>{t('usage_stats.model_price_empty')}</div>
          )}
        </div>
      </div>

      {/* Edit Modal */}
      <Modal
        open={editModel !== null}
        title={editModel ?? ''}
        onClose={() => setEditModel(null)}
        footer={
          <div className={styles.priceActions}>
            <Button variant="secondary" onClick={() => setEditModel(null)}>
              {t('common.cancel')}
            </Button>
            <Button variant="primary" onClick={handleSaveEdit}>
              {t('common.save')}
            </Button>
          </div>
        }
        width={420}
      >
        <div className={styles.editModalBody}>
          {MODEL_PRICE_FIELDS.map((field) => (
            <div key={field.key} className={styles.formField}>
              <label>{t(field.labelKey)} ($/1M)</label>
              <Input
                type="number"
                value={editFields[field.key]}
                onChange={(e) => updateEditField(field.key, e.target.value)}
                placeholder="0.00"
                step="0.0001"
              />
            </div>
          ))}
        </div>
      </Modal>
    </Card>
  );
}
