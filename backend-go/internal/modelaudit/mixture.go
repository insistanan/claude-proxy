package modelaudit

import (
	"fmt"
	"math"
	"math/rand"
	"sort"
)

type MixtureConfig struct {
	MaxIterations    int     `json:"maxIterations"`
	Tolerance        float64 `json:"tolerance"`
	BootstrapSamples int     `json:"bootstrapSamples"`
	ConfidenceLevel  float64 `json:"confidenceLevel"`
	Seed             int64   `json:"seed"`
}

type MixtureComponent struct {
	Candidate  string  `json:"candidate"`
	Proportion float64 `json:"proportion"`
	Lower      float64 `json:"lower"`
	Upper      float64 `json:"upper"`
}

type MixtureEstimate struct {
	Components    []MixtureComponent `json:"components"`
	LogLikelihood float64            `json:"logLikelihood"`
	Iterations    int                `json:"iterations"`
	Converged     bool               `json:"converged"`
}

func EstimateMixture(counts []int, candidates map[string][]float64, config MixtureConfig) (MixtureEstimate, error) {
	if config.MaxIterations <= 0 || config.BootstrapSamples <= 0 || config.Tolerance <= 0 ||
		math.IsNaN(config.Tolerance) || math.IsInf(config.Tolerance, 0) || config.ConfidenceLevel <= 0 || config.ConfidenceLevel >= 1 {
		return MixtureEstimate{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "混合估计配置无效")
	}
	names, distributions, total, err := prepareMixtureInput(counts, candidates)
	if err != nil {
		return MixtureEstimate{}, err
	}
	weights, logLikelihood, iterations, converged, err := fitMixture(counts, distributions, config.MaxIterations, config.Tolerance)
	if err != nil {
		return MixtureEstimate{}, err
	}
	bootstrap := make([][]float64, len(names))
	random := rand.New(rand.NewSource(config.Seed))
	mixtureDistribution := combineDistributions(weights, distributions)
	for sampleIndex := 0; sampleIndex < config.BootstrapSamples; sampleIndex++ {
		resampled := sampleCategoricalCounts(random, total, mixtureDistribution)
		resampledWeights, _, _, _, err := fitMixture(resampled, distributions, config.MaxIterations, config.Tolerance)
		if err != nil {
			return MixtureEstimate{}, fmt.Errorf("bootstrap %d 混合估计失败: %w", sampleIndex, err)
		}
		for candidateIndex, weight := range resampledWeights {
			bootstrap[candidateIndex] = append(bootstrap[candidateIndex], weight)
		}
	}
	tail := (1 - config.ConfidenceLevel) / 2
	components := make([]MixtureComponent, len(names))
	for index, name := range names {
		sort.Float64s(bootstrap[index])
		components[index] = MixtureComponent{
			Candidate: name, Proportion: weights[index],
			Lower: empiricalQuantile(bootstrap[index], tail), Upper: empiricalQuantile(bootstrap[index], 1-tail),
		}
	}
	return MixtureEstimate{
		Components: components, LogLikelihood: logLikelihood, Iterations: iterations, Converged: converged,
	}, nil
}

func prepareMixtureInput(counts []int, candidates map[string][]float64) ([]string, [][]float64, int, error) {
	if len(counts) == 0 || len(candidates) < 2 {
		return nil, nil, 0, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "混合估计至少需要一个观测维度和两个候选")
	}
	total := 0
	for _, count := range counts {
		if count < 0 {
			return nil, nil, 0, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "混合估计计数不能为负数")
		}
		total += count
	}
	if total == 0 {
		return nil, nil, 0, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "混合估计样本量必须大于 0")
	}
	names := make([]string, 0, len(candidates))
	for name := range candidates {
		names = append(names, name)
	}
	sort.Strings(names)
	distributions := make([][]float64, len(names))
	for index, name := range names {
		if name == "" {
			return nil, nil, 0, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "混合候选名称不能为空")
		}
		distribution, err := normalizedProbabilityVector(candidates[name])
		if err != nil || len(distribution) != len(counts) {
			return nil, nil, 0, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, fmt.Sprintf("混合候选 %q 分布无效", name), err)
		}
		distributions[index] = distribution
	}
	return names, distributions, total, nil
}

func fitMixture(counts []int, distributions [][]float64, maxIterations int, tolerance float64) ([]float64, float64, int, bool, error) {
	weights := make([]float64, len(distributions))
	for index := range weights {
		weights[index] = 1 / float64(len(weights))
	}
	total := 0
	for _, count := range counts {
		total += count
	}
	for iteration := 1; iteration <= maxIterations; iteration++ {
		updated := make([]float64, len(weights))
		for category, count := range counts {
			if count == 0 {
				continue
			}
			denominator := 0.0
			for candidate := range weights {
				denominator += weights[candidate] * distributions[candidate][category]
			}
			if denominator <= 0 {
				return nil, 0, iteration, false, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, fmt.Sprintf("观测类别 %d 在所有候选中概率为 0", category))
			}
			for candidate := range weights {
				responsibility := weights[candidate] * distributions[candidate][category] / denominator
				updated[candidate] += float64(count) * responsibility / float64(total)
			}
		}
		maxDifference := 0.0
		for index := range weights {
			maxDifference = math.Max(maxDifference, math.Abs(updated[index]-weights[index]))
		}
		weights = updated
		if maxDifference <= tolerance {
			return weights, mixtureLogLikelihood(counts, weights, distributions), iteration, true, nil
		}
	}
	return weights, mixtureLogLikelihood(counts, weights, distributions), maxIterations, false, nil
}

func combineDistributions(weights []float64, distributions [][]float64) []float64 {
	combined := make([]float64, len(distributions[0]))
	for candidate, weight := range weights {
		for category, probability := range distributions[candidate] {
			combined[category] += weight * probability
		}
	}
	return combined
}

func mixtureLogLikelihood(counts []int, weights []float64, distributions [][]float64) float64 {
	combined := combineDistributions(weights, distributions)
	total := 0.0
	for category, count := range counts {
		if count > 0 {
			total += float64(count) * math.Log(combined[category])
		}
	}
	return total
}

func sampleCategoricalCounts(random *rand.Rand, total int, probabilities []float64) []int {
	counts := make([]int, len(probabilities))
	for sample := 0; sample < total; sample++ {
		draw := random.Float64()
		cumulative := 0.0
		for category, probability := range probabilities {
			cumulative += probability
			if draw <= cumulative || category == len(probabilities)-1 {
				counts[category]++
				break
			}
		}
	}
	return counts
}

func empiricalQuantile(sortedValues []float64, probability float64) float64 {
	if len(sortedValues) == 1 {
		return sortedValues[0]
	}
	position := probability * float64(len(sortedValues)-1)
	lower := int(math.Floor(position))
	upper := int(math.Ceil(position))
	if lower == upper {
		return sortedValues[lower]
	}
	fraction := position - float64(lower)
	return sortedValues[lower]*(1-fraction) + sortedValues[upper]*fraction
}
